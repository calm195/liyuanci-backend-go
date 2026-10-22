package devapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestLocalKeyIsolation 追溯 T008/T011/T014、REQ-002/018：密钥实际包裹、用途隔离、篡改和取消失败关闭。
func TestLocalKeyIsolation(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	keys := &localKeys{bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), zap.New(core)}
	ctx := context.Background()
	secret := bytes.Repeat([]byte{3}, 32)
	ref, wrapped, err := keys.Wrap(ctx, secret)
	if err != nil || bytes.Contains(wrapped, secret) {
		t.Fatal("包裹失败或泄漏原始密钥")
	}
	plain, err := keys.Unwrap(ctx, ref, wrapped)
	if err != nil || !bytes.Equal(secret, plain) {
		t.Fatal("密钥往返失败")
	}
	wrapped[len(wrapped)-1] ^= 1
	if _, err = keys.Unwrap(ctx, ref, wrapped); err == nil {
		t.Fatal("篡改未拒绝")
	}
	if _, err = keys.Unwrap(ctx, "unknown", wrapped); err == nil {
		t.Fatal("未知引用未拒绝")
	}
	a, _ := keys.Digest(ctx, "local-lookup-v1", "openid", secret)
	b, _ := keys.Digest(ctx, "local-lookup-v1", "student_number", secret)
	if bytes.Equal(a, b) {
		t.Fatal("检索用途未隔离")
	}
	if _, err = keys.Digest(ctx, "unknown", "openid", secret); err == nil {
		t.Fatal("未知检索引用未拒绝")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = keys.Wrap(canceled, secret); err == nil {
		t.Fatal("取消未拒绝")
	}
	if logs.FilterField(zap.String("result", "failure")).Len() < 3 {
		t.Fatal("缺少失败完成日志")
	}
}

// TestObjectsPublishAtomically 追溯 T011/REQ-002/018：并发幂等、不可覆盖、精确版本、路径隔离及失败日志。
func TestObjectsPublishAtomically(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	objects := &localObjects{t.TempDir(), zap.New(core)}
	ctx := context.Background()
	data := []byte("encrypted-only")
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := objects.PutOnce(ctx, "../../escape", data); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	version, err := objects.PutOnce(ctx, "../../escape", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = objects.PutOnce(ctx, "../../escape", []byte("different")); err == nil {
		t.Fatal("覆盖未拒绝")
	}
	reader, err := objects.Open(ctx, "../../escape", version)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	reader.Close()
	if !bytes.Equal(data, got) {
		t.Fatal("对象读取错误")
	}
	if _, err = objects.Open(ctx, "../../escape", string(bytes.Repeat([]byte{'0'}, 64))); err == nil {
		t.Fatal("版本不匹配未拒绝")
	}
	files, _ := os.ReadDir(objects.root)
	if len(files) != 1 {
		t.Fatal("残留临时文件")
	}
	if logs.FilterField(zap.String("result", "failure")).Len() != 2 {
		t.Fatal("缺少失败完成日志")
	}
}

// TestSettingsRejectKeyReuse 追溯 T008/T011/T014、REQ-018：文件配置拒绝跨用途共钥及半套微信凭据。
func TestSettingsRejectKeyReuse(t *testing.T) {
	cfg := Settings{StudentDSN: "s", ReviewDSN: "r", WishDSN: "w", AdminDSN: "a", AdminPassword: "long-enough-password", ObjectDirectory: t.TempDir()}
	keys := []*[]byte{&cfg.WrapKey, &cfg.LookupKey, &cfg.SessionKey, &cfg.CardKey, &cfg.VerificationKey, &cfg.WishKey, &cfg.AdminKey}
	for i, key := range keys {
		*key = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}
	path := filepath.Join(t.TempDir(), "business.json")
	save := func() {
		data, _ := json.Marshal(cfg)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	cfg.CardKey = cfg.SessionKey
	save()
	if _, err := Load(path); err == nil {
		t.Fatal("复用密钥未拒绝")
	}
	cfg.CardKey = bytes.Repeat([]byte{4}, 32)
	cfg.WeChatAppID = "app"
	save()
	if _, err := Load(path); err == nil {
		t.Fatal("半套微信配置未拒绝")
	}
}
