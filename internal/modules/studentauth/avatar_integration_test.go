package studentauth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
)

type avatarFixture struct {
	puts        atomic.Int64
	unavailable atomic.Bool
	entered     chan struct{}
	resume      chan struct{}
}

func (f *avatarFixture) Scan(context.Context, []byte, string) (bool, error) {
	return true, nil
}

func (f *avatarFixture) PutAvatar(ctx context.Context, _ string, data []byte) (string, error) {
	if f.puts.Add(1) == 1 {
		close(f.entered)
		select {
		case <-f.resume:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.unavailable.Load() {
		return "", command.ErrUnavailable
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (f *avatarFixture) AvatarURL(_ context.Context, key string) (string, error) {
	return "https://synthetic.invalid/" + key, nil
}

func avatarPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 4, 4))
	pixels.Set(0, 0, c)
	var data bytes.Buffer
	if err := png.Encode(&data, pixels); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func testAvatarCommands(t *testing.T, service *studentauth.Service, fixture *avatarFixture, user studentauth.Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := avatarPNG(t, color.RGBA{R: 255, A: 255})
	second := avatarPNG(t, color.RGBA{B: 255, A: 255})
	upload := func(key string, version int64, data []byte) (studentauth.Me, error) {
		return service.UploadAvatar(ctx, user.Token, key, "", version, "image/png", bytes.NewReader(data))
	}

	type result struct {
		me  studentauth.Me
		err error
	}
	done := make(chan result, 1)
	go func() {
		me, err := upload("avatar-first", user.Me.Version, first)
		done <- result{me, err}
	}()
	select {
	case <-fixture.entered:
	case out := <-done:
		t.Fatalf("写入没有取得上传预约：%v", out.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := upload("avatar-first", user.Me.Version, first); !errors.Is(err, command.ErrConflict) {
		t.Fatalf("同键并发必须等待预约完成：%v", err)
	}
	close(fixture.resume)
	out := <-done
	if out.err != nil || out.me.AvatarID == nil || out.me.AvatarURL == "" || out.me.Version != user.Me.Version+1 {
		t.Fatalf("安装头像失败：%v", out.err)
	}
	replay, err := upload("avatar-first", user.Me.Version, first)
	if err != nil || replay.AvatarID == nil || *replay.AvatarID != *out.me.AvatarID || fixture.puts.Load() != 1 {
		t.Fatalf("已提交头像的重放产生外部写入：%v", err)
	}
	fixture.unavailable.Store(true)
	if _, err = upload("avatar-retry", out.me.Version, second); !errors.Is(err, command.ErrUnavailable) {
		t.Fatalf("外部写入故障未映射为不可用：%v", err)
	}
	current, err := service.Current(ctx, user.Token, "")
	if err != nil || current.AvatarID == nil || *current.AvatarID != *out.me.AvatarID || current.Version != out.me.Version {
		t.Fatalf("外部失败覆盖当前头像：%v", err)
	}
	fixture.unavailable.Store(false)
	updated, err := upload("avatar-retry", current.Version, second)
	if err != nil || updated.AvatarID == nil || *updated.AvatarID == *current.AvatarID {
		t.Fatalf("同键重试未安装新头像：%v", err)
	}
	removed, err := service.DeleteAvatar(ctx, user.Token, "avatar-remove", "", updated.Version)
	if err != nil || removed.AvatarID != nil || removed.AvatarURL != "" || removed.Version != updated.Version+1 {
		t.Fatalf("恢复默认头像失败：%v", err)
	}
}
