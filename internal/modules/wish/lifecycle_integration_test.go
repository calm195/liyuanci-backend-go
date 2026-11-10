package wish

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

type lifecycleFixture struct {
	t            *testing.T
	root         *gorm.DB
	service      *Service
	owner, token string
}

func lifecycleSetup(t *testing.T) *lifecycleFixture {
	t.Helper()
	dsn := testconfig.Value("wish_lifecycle_test_dsn")
	if dsn == "" {
		t.Skip("需隔离PG：按模块要求提供隔离依赖后运行相关集成测试")
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
	if e := testconfig.ApplyDatabaseRoles(context.Background(), root, log); e != nil {
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
	if e = root.Create(&entity.Session{
		ID:        uuid.NewString(),
		AccountID: &f.owner,
		TokenHash: h[:],
		AuthEpoch: 1,
		ExpiresAt: time.Now().Add(time.Hour),
	}).Error; e != nil {
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
	f.exec(
		"UPDATE public.content_registry SET status='approved',school_detection_state='completed',school_related=?,school_detection_version='synthetic-detector' WHERE id=?",
		school,
		a.registry.ID,
	)
	f.exec(
		"UPDATE public.wish_versions SET moderation_status='approved',school_detection_state='completed',school_related=?,school_detection_version='synthetic-detector',public_body='合成已审正文',identity_tags='{}' WHERE wish_id=? AND content_version=?",
		school,
		id,
		a.content.ContentVersion,
	)
	f.exec("UPDATE public.wishes SET state='ready',version=version+1 WHERE id=?", id)
}

func (f *lifecycleFixture) ready() CreatedDraft {
	f.t.Helper()
	out := f.create()
	out, e := f.service.Submit(
		context.Background(),
		f.token,
		uuid.NewString(),
		"submit",
		out.ID,
		out.Version,
		out.ContentVersion,
	)
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
	updated, e := s.Update(
		ctx,
		f.token,
		"edit",
		"edit",
		out.ID,
		UpdateInput{
			ContentInput:    ContentInput{Category: "other", Campus: "wushan", Body: "新合成正文", DurationDays: pointer(30)},
			ExpectedVersion: 1,
			RefreshExpiry:   true,
		},
	)
	if e != nil || updated.Version != 2 || updated.ContentVersion != 2 || updated.State != Draft {
		t.Fatal(updated, e)
	}
	if _, e = s.Update(ctx, f.token, "stale", "stale", out.ID, UpdateInput{ContentInput: draftInput(), ExpectedVersion: 1}); !errors.Is(e, command.ErrVersionConflict) {
		t.Fatal(e)
	}
	if replay, e := s.Update(
		ctx,
		f.token,
		"edit",
		"replay",
		out.ID,
		UpdateInput{
			ContentInput:    ContentInput{Category: "other", Campus: "wushan", Body: "新合成正文", DurationDays: pointer(30)},
			ExpectedVersion: 1,
			RefreshExpiry:   true,
		},
	); e != nil || replay != updated {
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
	if _, e := s.Update(
		ctx,
		f.token,
		"edit-fail",
		"edit",
		out.ID,
		UpdateInput{ContentInput: draftInput(), ExpectedVersion: 3},
	); !errors.Is(e, command.ErrUnavailable) {
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
	edited, e := s.Update(
		ctx,
		f.token,
		"edit-pending",
		"edit",
		out.ID,
		UpdateInput{ContentInput: draftInput(), ExpectedVersion: 3},
	)
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
	// 热度相同按发布时间与 ID 排序，关键词和排序变化后拒绝旧游标。
	filter := Query{Sort: "hot", Keyword: "已审正文", Limit: 2}
	first, e := s.List(ctx, "discovery", filter)
	if e != nil || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatal(first, e)
	}
	filter.Cursor = *first.NextCursor
	second, e := s.List(ctx, "discovery", filter)
	if e != nil || len(second.Items) != 1 || second.NextCursor != nil ||
		second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Fatal(second, e)
	}
	filter.Keyword = "不存在的合成搜索词"
	if _, e := s.List(ctx, "discovery", filter); !errors.Is(e, command.ErrInvalid) {
		t.Fatal("允许跨搜索词使用游标", e)
	}
	t.Run("周榜只计近期有效应愿", func(t *testing.T) {
		applicant, token := uuid.NewString(), uuid.NewString()
		f.exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'合成应愿人','verified')", applicant)
		hash := sha256.Sum256([]byte(token))
		if e := f.root.Create(&entity.Session{ID: uuid.NewString(), AccountID: &applicant,
			TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error; e != nil {
			t.Fatal(e)
		}
		target := published[2]
		application, e := s.Apply(ctx, token, uuid.NewString(), "discovery", target.ID,
			ApplicationInput{Message: "合成应愿说明", WishContentVersion: target.ContentVersion})
		if e != nil {
			t.Fatal(e)
		}
		before, e := s.Weekly(ctx, "discovery")
		if e != nil || len(before.Items) != 0 {
			t.Fatal("待审申请参与热度", before, e)
		}
		f.exec("UPDATE public.content_registry SET status='approved' WHERE kind='application' AND entity_id=?", application.ID)
		f.exec("UPDATE public.moderation_tasks SET status='approved',decision='approved' WHERE id=(SELECT review_task_id FROM public.applications WHERE id=?)", application.ID)
		f.exec("UPDATE public.applications SET status='active' WHERE id=?", application.ID)
		weekly, e := s.Weekly(ctx, "discovery")
		if e != nil || len(weekly.Items) != 1 || weekly.Items[0].ID != target.ID {
			t.Fatal("有效应愿未进入周榜", weekly, e)
		}
		f.exec("UPDATE public.applications SET created_at=clock_timestamp()-interval '8 days' WHERE id=?", application.ID)
		weekly, e = s.Weekly(ctx, "discovery")
		if e != nil || len(weekly.Items) != 0 {
			t.Fatal("历史应愿进入周榜", weekly, e)
		}
		total, e := s.List(ctx, "discovery", Query{Sort: "hot", Limit: 1})
		if e != nil || len(total.Items) != 1 || total.Items[0].ID != target.ID {
			t.Fatal("历史应愿未计入累计热度", total, e)
		}
	})
	t.Run("详情打开逐次计入累计和周榜", func(t *testing.T) {
		target := published[0]
		if _, e := s.Detail(ctx, "", "discovery", target.ID); e != nil {
			t.Fatal(e)
		}
		for range 2 {
			if _, e := s.OpenDetail(ctx, "", "discovery", target.ID); e != nil {
				t.Fatal(e)
			}
		}
		var count int64
		if e := f.root.Model(&entity.WishDetailView{}).Where("wish_id=?", target.ID).Count(&count).Error; e != nil || count != 2 {
			t.Fatal("两次打开必须各计一分，普通查询不计分", count, e)
		}
		weekly, e := s.Weekly(ctx, "discovery")
		if e != nil || len(weekly.Items) != 1 || weekly.Items[0].ID != target.ID {
			t.Fatal("详情打开未进入周榜", weekly, e)
		}
		f.exec("UPDATE public.wish_detail_views SET viewed_at=clock_timestamp()-interval '8 days' WHERE wish_id=?", target.ID)
		weekly, e = s.Weekly(ctx, "discovery")
		if e != nil || len(weekly.Items) != 0 {
			t.Fatal("历史详情打开进入周榜", weekly, e)
		}
		total, e := s.List(ctx, "discovery", Query{Sort: "hot", Limit: 1})
		if e != nil || len(total.Items) != 1 || total.Items[0].ID != target.ID {
			t.Fatal("两次历史详情打开应高于一次有效应愿", total, e)
		}
	})
	t.Run("心愿支持幂等、排序与周榜", func(t *testing.T) {
		target := published[1].ID
		if _, e := s.SetSupport(ctx, "", "support", target, true); !errors.Is(e, command.ErrUnauthenticated) {
			t.Fatal("游客能够支持", e)
		}
		// 作者可支持自己的心愿，并发重试均应只返回一票。
		var writes sync.WaitGroup
		for range 6 {
			writes.Go(func() {
				state, e := s.SetSupport(ctx, f.token, "support", target, true)
				if e != nil || !state.Supported || state.SupportCount != 1 {
					t.Errorf("重复支持未保持一票：%+v %v", state, e)
				}
			})
		}
		writes.Wait()
		detail, e := s.Detail(ctx, f.token, "support", target)
		if e != nil || detail.Support == nil || !detail.Support.Supported || detail.Support.SupportCount != 1 {
			t.Fatal("作者详情支持状态错误", detail.Support, e)
		}
		query := Query{Sort: SortSupport, ViewerToken: f.token, Limit: 1}
		list, e := s.List(ctx, "support", query)
		if e != nil || len(list.Items) != 1 || list.Items[0].ID != target || !list.Items[0].Supported || list.NextCursor == nil {
			t.Fatal("支持排序或本人状态错误", list, e)
		}
		query.Cursor, query.Limit = *list.NextCursor, 2
		list, e = s.List(ctx, "support", query)
		if e != nil || len(list.Items) != 2 || list.Items[0].ID == target || list.Items[1].ID == target {
			t.Fatal("支持排序游标错误", list, e)
		}
		weekly, e := s.Weekly(ctx, "support")
		if e != nil || len(weekly.Items) != 1 || weekly.Items[0].ID != target || weekly.Items[0].HeatScore != 1 {
			t.Fatal("支持应计入心愿周榜", weekly, e)
		}
		f.exec("UPDATE public.wish_supports SET created_at=clock_timestamp()-interval '8 days' WHERE wish_id=?", target)
		if _, e = s.SetSupport(ctx, f.token, "support", target, true); e != nil {
			t.Fatal(e)
		}
		weekly, e = s.Weekly(ctx, "support")
		if e != nil || len(weekly.Items) != 0 {
			t.Fatal("历史支持或重试不应计入周榜", weekly, e)
		}
		for range 2 {
			state, e := s.SetSupport(ctx, f.token, "support", target, false)
			if e != nil || state.Supported || state.SupportCount != 0 {
				t.Fatal("重复取消应保持零票", state, e)
			}
		}
	})
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
	if e := f.root.Create(&entity.Application{
		ID:                 appID,
		WishID:             published[1].ID,
		WishContentVersion: 1,
		ApplicantID:        applicant,
		Status:             "active",
		MessageCipher:      []byte(`{}`),
	}).Error; e != nil {
		t.Fatal(e)
	}
	workID := uuid.NewString()
	if e := f.root.Create(&entity.WorkItem{
		ID:          workID,
		Kind:        "application_scan",
		TargetKind:  "application",
		TargetID:    appID,
		OperationID: uuid.NewString(),
	}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.Update(
		ctx,
		f.token,
		"published-edit",
		"edit",
		published[1].ID,
		UpdateInput{ContentInput: draftInput(), ExpectedVersion: published[1].Version},
	); e != nil {
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

	// 应愿中跨原期限仍占一个名额，不能被到期查询释放。
	g := lifecycleSetup(t)
	matching := g.ready()
	g.exec("UPDATE public.wishes SET state='matching',published_at=clock_timestamp()-interval '8 days',expires_at=clock_timestamp()-interval '1 day' WHERE id=?", matching.ID)
	for range 2 {
		candidate := g.ready()
		v, e := g.service.Publish(ctx, g.token, uuid.NewString(), "matching-quota", candidate.ID, candidate.Version)
		if e != nil || v.State != Published {
			t.Fatal(v, e)
		}
	}
	candidate := g.ready()
	v, e := g.service.Publish(ctx, g.token, uuid.NewString(), "matching-quota-full", candidate.ID, candidate.Version)
	if e != nil || v.State != Ready {
		t.Fatal("应愿中没有占额度", v, e)
	}

	// 授权查询失败必须映射依赖故障，不能因错误被折叠成“不存在”而误报404。
	injected := false
	callback := "synthetic_wish_authorize_failure"
	if e := s.db.Callback().Query().Before("gorm:query").Register(callback, func(db *gorm.DB) {
		if !strings.HasSuffix(db.Statement.Table, "wishes") {
			return
		}
		where, ok := db.Statement.Clauses["WHERE"].Expression.(clause.Where)
		if !ok {
			return
		}
		for _, expression := range where.Exprs {
			if raw, ok := expression.(clause.Expr); ok && strings.Contains(raw.SQL, "owner_id") {
				injected = true
				db.AddError(gorm.ErrInvalidDB)
			}
		}
	}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.db.Callback().Query().Remove(callback) })
	if _, e := s.Update(
		ctx,
		f.token,
		uuid.NewString(),
		"authorize-failure",
		candidate.ID,
		UpdateInput{ContentInput: draftInput(), ExpectedVersion: candidate.Version},
	); !injected || !errors.Is(e, command.ErrUnavailable) {
		t.Fatal("授权故障映射错误", injected, e)
	}

}
