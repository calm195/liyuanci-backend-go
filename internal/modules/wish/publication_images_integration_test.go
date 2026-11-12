package wish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type wishImageMemory struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *wishImageMemory) PutOnce(_ context.Context, key string, data []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.data[key]; ok && !bytes.Equal(old, data) {
		return "", command.ErrConflict
	}
	m.data[key] = bytes.Clone(data)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func (m *wishImageMemory) Open(_ context.Context, key, version string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.data[key]
	if !ok {
		return nil, command.ErrNotFound
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != version {
		return nil, command.ErrUnavailable
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(data))), nil
}

type wishImageApproved struct{}

func (wishImageApproved) Scan(context.Context, []byte, string) (bool, error) { return true, nil }

// TestPostgresWishImages 验证真实数据库的图片引用、自动上架幂等与半匿名边界；扫描和存储为合成适配。
func TestPostgresWishImages(t *testing.T) {
	f := lifecycleSetup(t)
	ctx := context.Background()
	s := f.service
	objects := &wishImageMemory{data: map[string][]byte{}}
	s.SetImages(objects, wishImageApproved{}, nil)
	ids := []string{}
	for i := range 3 {
		pixels := image.NewRGBA(image.Rect(0, 0, 32, 32))
		pixels.Set(0, 0, color.RGBA{R: uint8(i + 1), A: 255})
		var b bytes.Buffer
		if e := png.Encode(&b, pixels); e != nil {
			t.Fatal(e)
		}
		key := uuid.NewString()
		uploaded, e := s.UploadImage(ctx, f.token, key, "image-test", "image/png", bytes.NewReader(b.Bytes()))
		if e != nil {
			t.Fatal(e)
		}
		replay, e := s.UploadImage(ctx, f.token, key, "image-replay", "image/png", bytes.NewReader(b.Bytes()))
		if e != nil || replay.ID != uploaded.ID {
			t.Fatal("上传幂等失败", replay, e)
		}
		ids = append(ids, uploaded.ID)
	}
	preview, e := s.ReadImage(ctx, f.token, "unattached-preview", ids[0], "")
	if e != nil || len(preview) == 0 {
		t.Fatal("未绑定上传不能预览", e)
	}
	clear(preview)
	input := draftInput()
	input.DurationDays = pointer(90)
	input.ImageIDs = ids
	input.IdentityMode = "semi"
	input.IdentityTags = &IdentityTags{Campus: pointer("wushan")}
	created, e := s.Create(ctx, f.token, uuid.NewString(), "with-images", input)
	if e != nil {
		t.Fatal(e)
	}
	pending, e := s.SubmitForPublication(ctx, f.token, uuid.NewString(), "submit", created.ID, created.Version, created.ContentVersion)
	if e != nil {
		t.Fatal(e)
	}
	f.approve(created.ID, false)
	publish := func() {
		t.Helper()
		e := f.root.Transaction(func(db *gorm.DB) error {
			state, e := ApplyApprovedPublication(ctx, db, created.ID, pending.ContentVersion)
			if e == nil && state != Published {
				t.Fatalf("未自动上架: %s", state)
			}
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	publish()
	publish()
	var row entity.Wish
	if e = f.root.Where("id=?", created.ID).Take(&row).Error; e != nil {
		t.Fatal(e)
	}
	if row.FirstPublishedAt == nil || row.ExpiresAt == nil || row.ExpiresAt.Sub(*row.FirstPublishedAt) != 90*24*time.Hour {
		t.Fatal("首次90天期限错误", row)
	}
	var n int64
	if e = f.root.Model(&entity.Notification{}).Where("target_ref=? AND type='wish_published'", created.ID).Count(&n).Error; e != nil || n != 1 {
		t.Fatal("发布重放重复通知", n, e)
	}
	public, e := s.Detail(ctx, "", "public", created.ID)
	if e != nil || public.Public == nil || len(public.Public.Images) != 3 {
		t.Fatal("图片未投影", public, e)
	}
	raw, _ := json.Marshal(public.Public)
	if public.Public.Author.ID != "" || public.Public.Author.ProfileAvailable || strings.Contains(string(raw), f.owner) {
		t.Fatal("半匿名泄露账号", string(raw))
	}
	if _, e = s.Detail(WithIdentityCapability(ctx, false), "", "legacy", created.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("旧客户端读取半匿名内容", e)
	}
	share, e := s.CreateShare(ctx, f.token, uuid.NewString(), "semi-share", ShareInput{TargetKind: "wish", ID: created.ID})
	if e != nil {
		t.Fatal(e)
	}
	shared, e := s.ResolveShare(ctx, "", "semi-share-read", share.Ref)
	if e != nil || shared.Target.Author.ID != "" || shared.Target.Author.ProfileAvailable || shared.GradeLabel != "" {
		t.Fatal("半匿名分享失败或补全未选择的年级", shared, e)
	}
	// 统计夹具只用于比较同一目标跨入口投影；浏览及收藏写入另由互动专项验证。
	f.exec("INSERT INTO public.content_statistics(target_kind,target_id,view_count) VALUES('wish',?,7)", created.ID)
	f.exec("INSERT INTO public.content_favorites(account_id,target_kind,target_id) VALUES(?,'wish',?)", f.owner, created.ID)
	sharedOwn, e := s.ResolveShare(ctx, f.token, "semi-share-own", share.Ref)
	if e != nil || sharedOwn.Target.ViewCount != 7 || !sharedOwn.Target.Favorited || !sharedOwn.Target.IsOwner {
		t.Fatal("分享未投影当前读者统计或本人状态", sharedOwn, e)
	}
	shared, e = s.ResolveShare(ctx, "", "semi-share-anonymous", share.Ref)
	if e != nil || shared.Target.ViewCount != 7 || shared.Target.Favorited || shared.Target.IsOwner {
		t.Fatal("分享复用作者收藏状态", shared, e)
	}
	profile, e := s.PublicProfile(ctx, "", "profile", f.owner, "", 20)
	if e != nil || len(profile.Items) != 0 {
		t.Fatal("主页泄露半匿名关联", profile, e)
	}
	data, e := s.ReadImage(ctx, "", "image", ids[0], created.ID)
	if e != nil || len(data) == 0 {
		t.Fatal("公开图片不可读", e)
	}
	clear(data)
	if _, e = s.ReadImage(WithIdentityCapability(ctx, false), "", "legacy-image", ids[0], created.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("旧客户端通过图片旁路读取半匿名内容", e)
	}
	if _, e = s.ReadImage(ctx, "", "unbound", ids[0], ""); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("无来源泄露图片", e)
	}
	own, e := s.Detail(ctx, f.token, "own", created.ID)
	if e != nil || own.OwnContent == nil {
		t.Fatal(e)
	}
	changed := *own.OwnContent
	changed.IdentityMode = "named"
	if _, e = s.Update(ctx, f.token, uuid.NewString(), "identity-change", created.ID, UpdateInput{ContentInput: changed, ExpectedVersion: own.Version}); !errors.Is(e, command.ErrConflict) {
		t.Fatal("首次公开后改变身份模式", e)
	}
	changed.IdentityMode = ""
	changed.ImageIDs = nil
	changed.DurationDays = pointer(30)
	if _, e = s.Update(WithIdentityCapability(ctx, false), f.token, uuid.NewString(), "legacy-duration", created.ID, UpdateInput{ContentInput: changed, ExpectedVersion: own.Version}); !errors.Is(e, command.ErrConflict) {
		t.Fatal("旧客户端静默截短90天", e)
	}
	changed.DurationDays = nil
	changed.Body = "合成补充后的主帖"
	edited, e := s.Update(ctx, f.token, uuid.NewString(), "edit", created.ID, UpdateInput{ContentInput: changed, ExpectedVersion: own.Version})
	if e != nil || edited.State != Draft {
		t.Fatal(edited, e)
	}
	var after entity.Wish
	if e = f.root.Where("id=?", created.ID).Take(&after).Error; e != nil || after.DurationDays != 90 || !after.ExpiresAt.Equal(*row.ExpiresAt) || !after.FirstPublishedAt.Equal(*row.FirstPublishedAt) {
		t.Fatal("普通改稿续期", e)
	}
	if _, e = s.ReadImage(ctx, "", "withdrawn-image", ids[0], created.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("下架图片仍公开", e)
	}
	updated, e := s.Detail(ctx, f.token, "updated", created.ID)
	if e != nil || len(updated.Images) != 3 || updated.OwnContent.IdentityMode != "semi" {
		t.Fatal("旧客户端改稿丢图片或匿名", e)
	}
	pending, e = s.SubmitForPublication(ctx, f.token, uuid.NewString(), "resubmit", created.ID, edited.Version, edited.ContentVersion)
	if e != nil {
		t.Fatal(e)
	}
	withdrawn, e := s.Withdraw(ctx, f.token, uuid.NewString(), "withdraw-pending", created.ID, pending.Version)
	if e != nil || withdrawn.State != Withdrawn {
		t.Fatal(withdrawn, e)
	}
	if e = f.root.Where("id=?", created.ID).Take(&after).Error; e != nil || after.PublishIntent {
		t.Fatal("撤回未清发布意愿", e)
	}
	var task entity.ModerationTask
	if e = f.root.Where("id=(SELECT review_task_id FROM public.wish_versions WHERE wish_id=? AND content_version=?)", created.ID, pending.ContentVersion).Take(&task).Error; e != nil || task.Status != "obsolete" {
		t.Fatal("撤回旧审核未失效", task.Status, e)
	}

	t.Run("无来源图片预览不能绕过换图删除和治理", func(t *testing.T) {
		sharedInput := draftInput()
		sharedInput.ImageIDs = ids[:1]
		sharedSource, e := s.Create(ctx, f.token, uuid.NewString(), "shared-image-source", sharedInput)
		if e != nil {
			t.Fatal(e)
		}
		restriction := uuid.NewString()
		f.exec(`INSERT INTO public.content_restrictions(id,content_id,kind,source_ref)
 SELECT ?,id,'violation',? FROM public.content_registry WHERE kind='wish' AND entity_id=? AND content_version=?`, restriction, uuid.NewString(), created.ID, pending.ContentVersion)
		if _, e = s.ReadImage(ctx, f.token, "restricted-preview", ids[0], ""); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("缓存上传URL绕过治理屏障", e)
		}
		if _, e = s.ReadImage(ctx, f.token, "restricted-shared-source", ids[0], sharedSource.ID); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("复制引用绕过原来源治理屏障", e)
		}
		if _, e = s.Create(ctx, f.token, uuid.NewString(), "copy-restricted-image", sharedInput); !errors.Is(e, command.ErrInvalid) {
			t.Fatal("受限图片仍可重新绑定", e)
		}
		f.exec("UPDATE public.content_restrictions SET active=false WHERE id=?", restriction)
		hold := uuid.NewString()
		f.exec("INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version) VALUES(?,'wish',?,1,?)", hold, created.ID, pending.ContentVersion+1)
		if _, e = s.ReadImage(ctx, f.token, "privacy-shared-source", ids[0], sharedSource.ID); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("复制引用绕过原来源隐私屏障", e)
		}
		if _, e = s.Create(ctx, f.token, uuid.NewString(), "copy-held-image", sharedInput); !errors.Is(e, command.ErrInvalid) {
			t.Fatal("隐私屏障图片仍可重新绑定", e)
		}
		f.exec("UPDATE public.privacy_holds SET status='released',released_at=clock_timestamp() WHERE id=?", hold)
		if e = s.Delete(ctx, f.token, uuid.NewString(), "delete-shared-source", sharedSource.ID, sharedSource.Version); e != nil {
			t.Fatal(e)
		}
		changed := *updated.OwnContent
		changed.ImageIDs = []string{}
		empty, e := s.Update(ctx, f.token, uuid.NewString(), "remove-images", created.ID, UpdateInput{ContentInput: changed, ExpectedVersion: withdrawn.Version})
		if e != nil || empty.State != Draft {
			t.Fatal(empty, e)
		}
		if _, e = s.ReadImage(ctx, f.token, "removed-preview", ids[0], ""); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("换图后旧上传URL仍可读", e)
		}
		copyInput := draftInput()
		copyInput.ImageIDs = ids[:1]
		copy, e := s.Create(ctx, f.token, uuid.NewString(), "delete-image-source", copyInput)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Delete(ctx, f.token, uuid.NewString(), "delete-image-source", copy.ID, copy.Version); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ReadImage(ctx, f.token, "deleted-preview", ids[0], ""); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("删除草稿后旧上传URL仍可读", e)
		}
	})
	t.Run("刷新期限回退保留图片和首次时间", verifyImageRefreshRollback)
}

func verifyImageRefreshRollback(t *testing.T) {
	f := lifecycleSetup(t)
	ctx, s := context.Background(), f.service
	s.SetImages(&wishImageMemory{data: map[string][]byte{}}, wishImageApproved{}, nil)
	var pngData bytes.Buffer
	if e := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 32, 32))); e != nil {
		t.Fatal(e)
	}
	uploaded, e := s.UploadImage(ctx, f.token, uuid.NewString(), "refresh-image", "image/png", &pngData)
	if e != nil {
		t.Fatal(e)
	}
	in := draftInput()
	in.ImageIDs = []string{uploaded.ID}
	in.DurationDays = pointer(90)
	w, e := s.Create(ctx, f.token, uuid.NewString(), "refresh-draft", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Submit(ctx, f.token, uuid.NewString(), "refresh-submit", w.ID, w.Version, w.ContentVersion); e != nil {
		t.Fatal(e)
	}
	f.approve(w.ID, false)
	ready, e := resolveCreated(ctx, f.root, w.ID, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	w, e = s.Publish(ctx, f.token, uuid.NewString(), "refresh-publish", w.ID, ready.Version)
	if e != nil {
		t.Fatal(e)
	}
	var before entity.Wish
	if e = f.root.Where("id=?", w.ID).Take(&before).Error; e != nil {
		t.Fatal(e)
	}
	// 匹配起点为合成夹具；回退、版本复制、复审后上架均执行正式服务及受限数据库角色。
	helper, match := uuid.NewString(), uuid.NewString()
	for _, row := range []any{
		&entity.Account{ID: helper, Verification: "verified", Nickname: "合成应愿人"},
		&entity.Match{ID: match, WishID: w.ID, RequesterID: f.owner, HelperID: helper},
		&entity.Conversation{ID: uuid.NewString(), MatchID: match},
	} {
		if e = f.root.Create(row).Error; e != nil {
			t.Fatal(e)
		}
	}
	f.exec("UPDATE public.wishes SET state='matching',version=version+1 WHERE id=?", w.ID)
	refreshed, e := s.Rollback(ctx, f.token, uuid.NewString(), "refresh-rollback", w.ID, RollbackInput{ExpectedVersion: w.Version + 1, Target: Published, RefreshExpiry: true})
	if e != nil || refreshed.State != PendingReview || refreshed.ContentVersion != w.ContentVersion+1 {
		t.Fatal(refreshed, e)
	}
	own, e := s.Detail(ctx, f.token, "refresh-own", w.ID)
	if e != nil || len(own.Images) != 1 || own.Images[0].ID != uploaded.ID || !own.FirstPublishedAt.Equal(*before.FirstPublishedAt) || !own.ExpiresAt.Equal(*before.ExpiresAt) {
		t.Fatal("刷新复审丢图片或提前改变期限", own, e)
	}
	f.approve(w.ID, false)
	ready, e = resolveCreated(ctx, f.root, w.ID, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Publish(ctx, f.token, uuid.NewString(), "refresh-republish", w.ID, ready.Version); e != nil {
		t.Fatal(e)
	}
	public, e := s.Detail(ctx, "", "refresh-public", w.ID)
	if e != nil || public.Public == nil || len(public.Public.Images) != 1 || public.Public.Images[0].ID != uploaded.ID || !public.Public.FirstPublishedAt.Equal(*before.FirstPublishedAt) {
		t.Fatal("刷新后公开图片或首次时间错误", public, e)
	}
	f.exec("INSERT INTO public.content_statistics(target_kind,target_id,view_count) VALUES('wish',?,9)", w.ID)
	f.exec("INSERT INTO public.content_favorites(account_id,target_kind,target_id) VALUES(?,'wish',?)", f.owner, w.ID)
	profile, e := s.PublicProfile(ctx, f.token, "profile-own-stats", f.owner, "", 20)
	if e != nil || len(profile.Items) != 1 || profile.Items[0].Content.ViewCount != 9 || !profile.Items[0].Content.Favorited || !profile.Items[0].Content.IsOwner {
		t.Fatal("公开主页未投影当前读者互动状态", profile, e)
	}
	anonymous, e := s.PublicProfile(ctx, "", "profile-anonymous-stats", f.owner, "", 20)
	if e != nil || len(anonymous.Items) != 1 || anonymous.Items[0].Content.ViewCount != 9 || anonymous.Items[0].Content.Favorited || anonymous.Items[0].Content.IsOwner {
		t.Fatal("公开主页泄露作者收藏或本人状态", anonymous, e)
	}
}
