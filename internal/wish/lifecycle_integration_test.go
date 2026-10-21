package wish

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

type lifecycleFixture struct {
	t            *testing.T
	root         *gorm.DB
	service      *Service
	owner, token string
}

func lifecycleSetup(t *testing.T) *lifecycleFixture {
	t.Helper()
	dsn := os.Getenv("APP_WISH_LIFECYCLE_TEST_DSN")
	if dsn == "" {
		t.Skip("需隔离PG：python3 scripts/check-wish.py")
	}
	open := func(role string) *gorm.DB {
		t.Helper()
		connection := dsn
		if role != "" {
			connection += " options='-c role=" + role + "'"
		}
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: connection, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if e != nil {
			t.Fatal("连接失败")
		}
		p, _ := db.DB()
		t.Cleanup(func() { p.Close() })
		return db
	}
	root := open("")
	log := zap.NewNop()
	if e := migration.Run(context.Background(), root, log, "up"); e != nil {
		t.Fatal(e)
	}
	if e := migration.ApplyPermissions(context.Background(), root, log); e != nil {
		t.Fatal(e)
	}
	codec, _ := securedata.New(&testKeys{}, log)
	cfg := serviceConfig()
	cfg.Command.Timeout = 15 * time.Second
	s, e := NewService(open("liyuanci_api"), log, codec, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f := &lifecycleFixture{t: t, root: root, service: s, owner: uuid.NewString(), token: uuid.NewString()}
	if e = root.Create(&entity.Account{ID: f.owner, Verification: "verified", Nickname: "合成作者"}).Error; e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte(f.token))
	if e = root.Create(&entity.Session{ID: uuid.NewString(), AccountID: &f.owner, TokenHash: h[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error; e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *lifecycleFixture) exec(q string, args ...any) {
	f.t.Helper()
	if e := f.root.Exec(q, args...).Error; e != nil {
		f.t.Fatal(e)
	}
}
func (f *lifecycleFixture) create() CreatedDraft {
	f.t.Helper()
	out, e := f.service.Create(context.Background(), f.token, uuid.NewString(), "create", draftInput())
	if e != nil {
		f.t.Fatal(e)
	}
	return out
}

// approve 仅在测试迁移身份中构造审核结果，不实现或冒充真实审核供应商。
func (f *lifecycleFixture) approve(id string, school bool) {
	f.t.Helper()
	a, e := readAggregate(context.Background(), f.root, id, false)
	if e != nil {
		f.t.Fatal(e)
	}
	if a.content.ReviewTaskID == nil {
		f.t.Fatal("未送审")
	}
	f.exec("UPDATE public.moderation_tasks SET status='approved',decision='approved' WHERE id=?", *a.content.ReviewTaskID)
	f.exec("UPDATE public.content_registry SET status='approved',school_detection_state='completed',school_related=?,school_detection_version='synthetic-detector' WHERE id=?", school, a.registry.ID)
	f.exec("UPDATE public.wish_versions SET moderation_status='approved',school_detection_state='completed',school_related=?,school_detection_version='synthetic-detector',public_body='合成已审正文',identity_tags='{}' WHERE wish_id=? AND content_version=?", school, id, a.content.ContentVersion)
	f.exec("UPDATE public.wishes SET state='ready',version=version+1 WHERE id=?", id)
}
func (f *lifecycleFixture) ready() CreatedDraft {
	f.t.Helper()
	out := f.create()
	out, e := f.service.Submit(context.Background(), f.token, uuid.NewString(), "submit", out.ID, out.Version, out.ContentVersion)
	if e != nil {
		f.t.Fatal(e)
	}
	f.approve(out.ID, false)
	out, e = resolveCreated(context.Background(), f.root, out.ID, f.owner)
	if e != nil {
		f.t.Fatal(e)
	}
	return out
}

// TestPostgresLifecycle 追溯 T014/REQ-003/004/005/008/018：编辑/删除/送审与上架均使用真实运行角色和同一根事务。
func TestPostgresLifecycle(t *testing.T) {
	f := lifecycleSetup(t)
	s := f.service
	ctx := context.Background()
	out := f.create()
	if _, e := readAggregate(ctx, s.db, out.ID, false); e != nil {
		t.Fatal("读取聚合失败", e)
	}
	updated, e := s.Update(ctx, f.token, "edit", "edit", out.ID, UpdateInput{ContentInput: ContentInput{Category: "other", Campus: "wushan", Body: "新合成正文", DurationDays: pointer(30)}, ExpectedVersion: 1, RefreshExpiry: true})
	if e != nil || updated.Version != 2 || updated.ContentVersion != 2 || updated.State != Draft {
		t.Fatal(updated, e)
	}
	if _, e = s.Update(ctx, f.token, "stale", "stale", out.ID, UpdateInput{ContentInput: draftInput(), ExpectedVersion: 1}); !errors.Is(e, command.ErrVersionConflict) {
		t.Fatal(e)
	}
	if replay, e := s.Update(ctx, f.token, "edit", "replay", out.ID, UpdateInput{ContentInput: ContentInput{Category: "other", Campus: "wushan", Body: "新合成正文", DurationDays: pointer(30)}, ExpectedVersion: 1, RefreshExpiry: true}); e != nil || replay != updated {
		t.Fatal(replay, e)
	}
	var old entity.WishVersion
	if e = f.root.Where("wish_id=? AND content_version=1", out.ID).Take(&old).Error; e != nil || old.ModerationStatus != "obsolete" {
		t.Fatal(old, e)
	}
	submitted, e := s.Submit(ctx, f.token, "submit", "submit", out.ID, 2, 2)
	if e != nil || submitted.State != PendingReview || submitted.Version != 3 {
		t.Fatal(submitted, e)
	}
	var n int64
	f.root.Model(&entity.WorkItem{}).Where("kind='wish_review'").Count(&n)
	if n != 1 {
		t.Fatal("送审未原子追加工作", n)
	}
	if replay, e := s.Submit(ctx, f.token, "submit", "replay", out.ID, 2, 2); e != nil || replay != submitted {
		t.Fatal(replay, e)
	}
	// 密钥失败发生在旧审核作废之后，必须把作废和版本写入一起回滚。
	keys := s.codec
	failedCodec, _ := securedata.New(&testKeys{failed: true}, zap.NewNop())
	s.codec = failedCodec
	if _, e := s.Update(ctx, f.token, "edit-fail", "edit", out.ID, UpdateInput{ContentInput: draftInput(), ExpectedVersion: 3}); !errors.Is(e, command.ErrUnavailable) {
		t.Fatal(e)
	}
	s.codec = keys
	var unchanged entity.Wish
	if e := f.root.Where("id=?", out.ID).Take(&unchanged).Error; e != nil || unchanged.Version != 3 {
		t.Fatal("编辑失败未回滚", unchanged, e)
	}
	var pending entity.WorkItem
	if e := f.root.Where("kind='wish_review'").Take(&pending).Error; e != nil || pending.Status != "pending" {
		t.Fatal("工作取消未回滚", pending, e)
	}
	// 编辑待审内容会取消旧工作，旧task不能成为新版本的审批依据。
	edited, e := s.Update(ctx, f.token, "edit-pending", "edit", out.ID, UpdateInput{ContentInput: draftInput(), ExpectedVersion: 3})
	if e != nil {
		t.Fatal(e)
	}
	var work entity.WorkItem
	if e = f.root.Where("kind='wish_review'").Take(&work).Error; e != nil || work.Status != "cancelled" {
		t.Fatal(work, e)
	}
	if e = s.Delete(ctx, f.token, "delete", "delete", out.ID, edited.Version); e != nil {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, f.token, "delete", "replay", out.ID, edited.Version); e != nil {
		t.Fatal("删除未幂等", e)
	}
	if _, e = s.Create(ctx, f.token, "new", "new", draftInput()); e != nil {
		t.Fatal(e)
	}
	if _, e = resolveCreated(ctx, f.root, out.ID, f.owner); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("已删除仍可访问", e)
	}
	var versions []entity.WishVersion
	f.root.Where("wish_id=?", out.ID).Find(&versions)
	for _, v := range versions {
		if string(v.BodyCipher) != "{}" {
			t.Fatal("已删除仍有密文")
		}
	}
	// 四条不同心愿并发竞争同一作者额度，最多三条上架，满额保持ready且不提前计时。
	ready := make([]CreatedDraft, 4)
	for i := range ready {
		ready[i] = f.ready()
	}
	var wg sync.WaitGroup
	results := make(chan CreatedDraft, 4)
	failures := make(chan error, 4)
	for _, r := range ready {
		wg.Add(1)
		go func(r CreatedDraft) {
			defer wg.Done()
			v, e := s.Publish(ctx, f.token, uuid.NewString(), "publish", r.ID, r.Version)
			results <- v
			failures <- e
		}(r)
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	published := []CreatedDraft{}
	var waiting CreatedDraft
	for r := range results {
		if r.State == Published {
			published = append(published, r)
		} else if r.State == Ready {
			waiting = r
		} else {
			t.Fatal(r)
		}
	}
	if len(published) != 3 || waiting.ID == "" {
		t.Fatal("额度错误", len(published), waiting)
	}
	var row entity.Wish
	f.root.Where("id=?", waiting.ID).Take(&row)
	if row.ExpiresAt != nil || row.PublishedAt != nil {
		t.Fatal("满额提前计时")
	}
	// 撤下只作废申请，不清除已通过审核；重新编辑后仍需要新审核。
	withdrawn, e := s.Withdraw(ctx, f.token, "withdraw", "withdraw", published[0].ID, published[0].Version)
	if e != nil || withdrawn.State != Withdrawn {
		t.Fatal(withdrawn, e)
	}
	if e = s.Delete(ctx, f.token, "not-draft", "delete", withdrawn.ID, withdrawn.Version); !errors.Is(e, command.ErrConflict) {
		t.Fatal(e)
	}
	waiting, e = s.Publish(ctx, f.token, "publish-again", "publish", waiting.ID, waiting.Version)
	if e != nil || waiting.State != Published {
		t.Fatal(waiting, e)
	}
	// 到期由数据库当前时刻判断，额度满时仍优先返回expired。
	r := f.ready()
	f.exec("UPDATE public.wishes SET expires_at=clock_timestamp()-interval '1 second' WHERE id=?", r.ID)
	expired, e := s.Publish(ctx, f.token, "expired", "expired", r.ID, r.Version)
	if e != nil || expired.State != Expired {
		t.Fatal(expired, e)
	}
	// 审核仅有approved字符串不足以公开：旧限制代次和未人工涉校结果都必须失败关闭。
	r = f.ready()
	f.exec("UPDATE public.wishes SET restriction_epoch=restriction_epoch+1 WHERE id=?", r.ID)
	if _, e = s.Publish(ctx, f.token, "old-approval", "publish", r.ID, r.Version); !errors.Is(e, command.ErrConflict) {
		t.Fatal(e)
	}
	r = f.ready()
	a, e := readAggregate(ctx, f.root, r.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	f.exec("UPDATE public.wish_versions SET school_related=true WHERE wish_id=?", r.ID)
	f.exec("UPDATE public.content_registry SET school_related=true WHERE id=?", a.registry.ID)
	if _, e = s.Publish(ctx, f.token, "school", "school", r.ID, r.Version); !errors.Is(e, command.ErrConflict) {
		t.Fatal(e)
	}
	// 运行身份不能把任务改为人工通过或改写审核意见。
	if e = s.db.Exec("UPDATE public.moderation_tasks SET decision='approved' WHERE id=?", *a.content.ReviewTaskID).Error; e == nil {
		t.Fatal("学生运行角色能写审核结论")
	}

	// 编辑已上架心愿原子作废旧申请及其工作源。
	applicant := uuid.NewString()
	if e := f.root.Create(&entity.Account{ID: applicant, Nickname: "合成应愿人"}).Error; e != nil {
		t.Fatal(e)
	}
	appID := uuid.NewString()
	if e := f.root.Create(&entity.Application{ID: appID, WishID: published[1].ID, WishContentVersion: 1, ApplicantID: applicant, Status: "active", MessageCipher: []byte(`{}`)}).Error; e != nil {
		t.Fatal(e)
	}
	workID := uuid.NewString()
	if e := f.root.Create(&entity.WorkItem{ID: workID, Kind: "application_scan", TargetKind: "application", TargetID: appID, OperationID: uuid.NewString()}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.Update(ctx, f.token, "published-edit", "edit", published[1].ID, UpdateInput{ContentInput: draftInput(), ExpectedVersion: published[1].Version}); e != nil {
		t.Fatal(e)
	}
	var app entity.Application
	if e := f.root.Where("id=?", appID).Take(&app).Error; e != nil || app.Status != "invalidated" || app.Version != 2 {
		t.Fatal(app, e)
	}
	var cancelled entity.WorkItem
	if e := f.root.Where("id=?", workID).Take(&cancelled).Error; e != nil || cancelled.Status != "cancelled" {
		t.Fatal(cancelled, e)
	}

}
