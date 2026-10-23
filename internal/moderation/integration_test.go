package moderation_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

type testKeys struct{}

func (testKeys) Wrap(_ context.Context, data []byte) (string, []byte, error) {
	b, _ := aes.NewCipher(bytes.Repeat([]byte{41}, 32))
	g, _ := cipher.NewGCM(b)
	n := make([]byte, g.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return "", nil, e
	}
	return "synthetic-wrap", append(n, g.Seal(nil, n, data, nil)...), nil
}
func (testKeys) Unwrap(_ context.Context, ref string, data []byte) ([]byte, error) {
	b, _ := aes.NewCipher(bytes.Repeat([]byte{41}, 32))
	g, _ := cipher.NewGCM(b)
	if ref != "synthetic-wrap" || len(data) < g.NonceSize() {
		return nil, errors.New("synthetic-key-error")
	}
	return g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], nil)
}
func (testKeys) Digest(_ context.Context, ref, purpose string, data []byte) ([]byte, error) {
	return securedata.LookupDigest(bytes.Repeat([]byte{42}, 32), purpose, string(data))
}

type analyzer struct {
	mu    sync.Mutex
	fail  bool
	hook  func()
	calls int
}

func (a *analyzer) Analyze(ctx context.Context, input wish.Content) (moderation.Assessment, error) {
	a.mu.Lock()
	a.calls++
	failure, hook := a.fail, a.hook
	a.mu.Unlock()
	if hook != nil {
		hook()
	}
	if ctx.Err() != nil || failure {
		return moderation.Assessment{}, errors.New("SYNTHETIC_PROVIDER_SECRET")
	}
	return moderation.Assessment{Body: strings.ReplaceAll(input.Body, "合成个人姓名", "[已脱敏]"), Tags: input.IdentityTags, OrdinaryHit: strings.Contains(input.Body, "隐私命中"), DetectorVersion: "synthetic-v1"}, nil
}
func (a *analyzer) set(failure bool, hook func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail = failure
	a.hook = hook
}

type handler struct {
	worker *moderation.Worker
	seen   chan durable.Consumption
	mu     sync.Mutex
	failed bool
}

func (h *handler) Consume(ctx context.Context, event durable.Event) (durable.Consumption, error) {
	out, e := h.worker.Consume(ctx, event)
	select {
	case h.seen <- out:
	case <-ctx.Done():
		return durable.Consumption{}, durable.ErrTemporary
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if e == nil && out.Applied && !h.failed {
		h.failed = true
		return durable.Consumption{}, durable.ErrTemporary
	}
	return out, e
}

// TestModerationCore 验证 T016/REQ-008、015、017、018、019 的真实PG/MQ核心，外部文本分析明确合成。
func TestModerationCore(t *testing.T) {
	dsn := os.Getenv("APP_MODERATION_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PG，使用make integration执行")
	}
	ctx := context.Background()
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	open := func(value string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if e != nil {
			t.Fatal("连接隔离数据库失败")
		}
		pool, _ := db.DB()
		t.Cleanup(func() { pool.Close() })
		return db
	}
	parent := open(dsn)
	database := "moderation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if !strings.Contains(dsn, "dbname=liyuanci ") {
		t.Fatal("需要指定隔离集成DSN格式")
	}
	if parent.Exec("CREATE DATABASE "+database).Error != nil {
		t.Fatal("创建专用测试数据库失败")
	}
	dsn = strings.Replace(dsn, "dbname=liyuanci ", "dbname="+database+" ", 1)
	root := open(dsn)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(migration.Run(ctx, root, logger, "up"))
	must(migration.ApplyPermissions(ctx, root, logger))
	// 空库上下行、权限重放与中文对象注释，保留Docker卷及专用测试数据库供追踪。
	must(migration.Run(ctx, root, logger, "down"))
	must(migration.Run(ctx, root, logger, "up"))
	must(migration.ApplyPermissions(ctx, root, logger))
	must(migration.ApplyPermissions(ctx, root, logger))
	var missing int64
	must(root.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname IN ('public','evidence') AND c.relkind='r' AND (coalesce(obj_description(c.oid,'pg_class'),'')='' OR coalesce(col_description(c.oid,a.attnum),'')='')`).Scan(&missing).Error)
	if missing != 0 {
		t.Fatal("缺表字段中文注释")
	}
	roleDB := func(role string) *gorm.DB { return open(dsn + " options='-c role=" + role + "'") }
	apiDB, adminDB, workerDB := roleDB("liyuanci_api"), roleDB("liyuanci_admin_api"), roleDB("liyuanci_domain_worker")
	codec, e := securedata.New(testKeys{}, logger)
	must(e)
	commandConfig := command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}
	service, e := moderation.New(adminDB, logger, codec, moderation.Config{Command: commandConfig, CursorKey: bytes.Repeat([]byte{50}, 32), MaxEntries: 100})
	must(e)
	wishes, e := wish.NewService(apiDB, logger, codec, nil, wish.ServiceConfig{Command: commandConfig, ReceiptKey: bytes.Repeat([]byte{51}, 32)})
	must(e)
	provider := &analyzer{}
	worker, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{AnalysisTimeout: 5 * time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}})
	must(e)
	admin, adminToken, owner, studentToken := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	credential := "synthetic-admin"
	must(root.Create(&entity.AdminAccount{ID: admin, CredentialRef: &credential}).Error)
	must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "review_operator"}).Error)
	hash := sha256.Sum256([]byte(adminToken))
	must(root.Create(&entity.Session{ID: uuid.NewString(), AdminID: &admin, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
	must(root.Create(&entity.Account{ID: owner, Nickname: "合成作者", Verification: "verified"}).Error)
	hash = sha256.Sum256([]byte(studentToken))
	must(root.Create(&entity.Session{ID: uuid.NewString(), AccountID: &owner, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
	head := func(k string) entity.WordlistHead {
		t.Helper()
		out, e := service.Wordlists(ctx, adminToken, "heads", "", 100)
		must(e)
		for _, h := range out.Heads {
			if h.Kind == k {
				return h
			}
		}
		t.Fatal("缺基准")
		return entity.WordlistHead{}
	}
	publish := func(k string, words []string) moderation.Wordlist {
		t.Helper()
		h := head(k)
		draft, e := service.CreateWordlist(ctx, adminToken, uuid.NewString(), "create-rule", moderation.WordlistInput{Kind: k, Entries: words, ExpectedVersion: h.Version})
		must(e)
		h = head(k)
		out, e := service.PublishWordlist(ctx, adminToken, uuid.NewString(), "publish-rule", draft.ID, moderation.WordlistPublish{ExpectedVersion: draft.Version, ExpectedHeadVersion: h.Version})
		must(e)
		return out
	}
	submit := func(body string) (wish.CreatedDraft, durable.Event) {
		t.Helper()
		draft, e := wishes.Create(ctx, studentToken, uuid.NewString(), "wish-create", wish.ContentInput{Category: "help", Campus: "wushan", Body: body})
		must(e)
		draft, e = wishes.Submit(ctx, studentToken, uuid.NewString(), "wish-submit", draft.ID, draft.Version, draft.ContentVersion)
		must(e)
		var event durable.Event
		must(root.Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.entity_id=? AND e.superseded_at IS NULL`, draft.ID).Scan(&event).Error)
		if event.EventID == "" {
			t.Fatal("未持久化事件")
		}
		return draft, event
	}
	task := func(id string) entity.ModerationTask {
		t.Helper()
		var row entity.ModerationTask
		must(root.Where("id=?", id).Take(&row).Error)
		return row
	}
	state := func(id string) entity.Wish {
		t.Helper()
		var row entity.Wish
		must(root.Where("id=?", id).Take(&row).Error)
		return row
	}
	checkReceipt := func(event durable.Event) {
		t.Helper()
		var n int64
		must(root.Model(&entity.ConsumerReceipt{}).Table("public.consumer_receipts AS r").Joins("JOIN public.work_items w ON w.operation_id=r.operation_id").Where("w.id=? AND r.consumer_kind='wish_review.scan'", event.CorrelationID).Count(&n).Error)
		if n != 1 {
			t.Fatal("结果与领域回执未唯一提交")
		}
	}
	t.Run("无正式发布规则失败关闭", func(t *testing.T) {
		_, event := submit("正常合成正文")
		if _, e := worker.Consume(ctx, event); e == nil {
			t.Fatal("缺词库仍放行")
		}
		if row := task(event.AggregateID); row.Status != "pending" {
			t.Fatal("失败改变审核结论")
		}
		if provider.calls != 0 {
			t.Fatal("缺规则仍调用供应商")
		}
	})
	ordinary := publish("ordinary_violation", []string{"普通命中"})
	publish("school", []string{"校务标记"})
	publish("crisis", []string{"危机标记"})
	t.Run("双版本完整快照及旧基准拒绝", func(t *testing.T) {
		h := head("ordinary_violation")
		a, e := service.CreateWordlist(ctx, adminToken, "same-rule-key", "draft-a", moderation.WordlistInput{Kind: h.Kind, Entries: []string{"普通命中"}, ExpectedVersion: h.Version})
		must(e)
		again, e := service.CreateWordlist(ctx, adminToken, "same-rule-key", "draft-replay", moderation.WordlistInput{Kind: h.Kind, Entries: []string{"普通命中"}, ExpectedVersion: h.Version})
		must(e)
		if a.ID != again.ID {
			t.Fatal("重放新建了快照")
		}
		h = head(h.Kind)
		b, e := service.CreateWordlist(ctx, adminToken, uuid.NewString(), "draft-b", moderation.WordlistInput{Kind: h.Kind, Entries: []string{"普通命中", "新增标记"}, ExpectedVersion: h.Version})
		must(e)
		h = head(h.Kind)
		_, e = service.PublishWordlist(ctx, adminToken, uuid.NewString(), "stale-version", b.ID, moderation.WordlistPublish{ExpectedVersion: 99, ExpectedHeadVersion: h.Version})
		if e != command.ErrVersionConflict {
			t.Fatal("草案版本未核验", e)
		}
		_, e = service.PublishWordlist(ctx, adminToken, uuid.NewString(), "publish-b", b.ID, moderation.WordlistPublish{ExpectedVersion: b.Version, ExpectedHeadVersion: h.Version})
		must(e)
		h = head(h.Kind)
		_, e = service.PublishWordlist(ctx, adminToken, uuid.NewString(), "stale-base", a.ID, moderation.WordlistPublish{ExpectedVersion: a.Version, ExpectedHeadVersion: h.Version})
		if e != command.ErrConflict {
			t.Fatal("旧生效基准覆盖新规则", e)
		}
		old, e := service.Wordlist(ctx, adminToken, "archive-detail", ordinary.ID)
		must(e)
		if old.Status != "archived" {
			t.Fatal("旧版未归档")
		}
		_, e = service.PublishWordlist(ctx, adminToken, uuid.NewString(), "archive-publish", old.ID, moderation.WordlistPublish{ExpectedVersion: old.Version, ExpectedHeadVersion: h.Version})
		if e != command.ErrConflict {
			t.Fatal("归档可重新发布", e)
		}
	})
	t.Run("正常扫描只进入待上架并脱敏", func(t *testing.T) {
		draft, event := submit("合成个人姓名需要帮助")
		out, e := worker.Consume(ctx, event)
		must(e)
		if !out.Applied || state(draft.ID).State != "ready" {
			t.Fatal("正常扫描未进入ready")
		}
		row := state(draft.ID)
		if row.PublishedAt != nil {
			t.Fatal("审核自动上架")
		}
		checkReceipt(event)
		out, e = worker.Consume(ctx, event)
		must(e)
		if !out.Replayed {
			t.Fatal("重复未去重")
		}
		var v entity.WishVersion
		must(root.Where("wish_id=? AND content_version=1", draft.ID).Take(&v).Error)
		if v.PublicBody == nil || strings.Contains(*v.PublicBody, "合成个人姓名") {
			t.Fatal("未使用脱敏候选")
		}
	})
	t.Run("涉校人工审批与主动上架提示", func(t *testing.T) {
		draft, event := submit("校务标记需要讨论")
		out, e := worker.Consume(ctx, event)
		must(e)
		if !out.Applied || state(draft.ID).State != "pending_review" {
			t.Fatal("涉校被自动放行")
		}
		detail, e := service.Case(ctx, adminToken, "case-detail", event.AggregateID)
		must(e)
		if !detail.SchoolRelated || detail.ContentVersion != 1 || detail.Version == 1 {
			t.Fatal("案件版本或涉校判定不符")
		}
		outcome, e := service.Decide(ctx, adminToken, "approve-school", "decision", detail.ID, moderation.Decision{Decision: "approved", ExpectedVersion: detail.Version})
		must(e)
		if outcome.BusinessOutcome != "ready" {
			t.Fatal("人工审核自动公开")
		}
		_, e = service.Decide(ctx, adminToken, "approve-school", "decision-replay", detail.ID, moderation.Decision{Decision: "approved", ExpectedVersion: detail.Version})
		must(e)
		row := state(draft.ID)
		_, e = wishes.Publish(ctx, studentToken, uuid.NewString(), "publish", row.ID, row.Version)
		must(e)
		public, e := wishes.Detail(ctx, "", "public", row.ID)
		must(e)
		if public.Public == nil || public.Public.NoticeText != "未经证实" || !public.Public.SchoolRelated {
			t.Fatal("涉校公开提示缺失")
		}
	})
	t.Run("驳回原因建议及旧案件拒绝", func(t *testing.T) {
		draft, event := submit("普通命中需要修改")
		_, e := worker.Consume(ctx, event)
		must(e)
		detail, e := service.Case(ctx, adminToken, "detail", event.AggregateID)
		must(e)
		_, e = service.Decide(ctx, adminToken, uuid.NewString(), "missing-suggestion", detail.ID, moderation.Decision{Decision: "rejected", Reason: "原因", ExpectedVersion: detail.Version})
		if e != command.ErrInvalid {
			t.Fatal("缺建议被接受")
		}
		_, e = service.Decide(ctx, adminToken, "reject", "reject", detail.ID, moderation.Decision{Decision: "rejected", Reason: "合成原因", RevisionSuggestion: "修改后重提", ExpectedVersion: detail.Version})
		must(e)
		if state(draft.ID).State != "rejected" || task(detail.ID).RevisionSuggestion == nil {
			t.Fatal("驳回结果丢失")
		}
		row := state(draft.ID)
		_, e = wishes.Update(ctx, studentToken, uuid.NewString(), "edit", row.ID, wish.UpdateInput{ContentInput: wish.ContentInput{Category: "help", Campus: "wushan", Body: "修改后的内容"}, ExpectedVersion: row.Version})
		must(e)
		_, e = service.Case(ctx, adminToken, "old-detail", detail.ID)
		if e != command.ErrNotFound {
			t.Fatal("旧案件可读取新正文", e)
		}
		_, e = service.Decide(ctx, adminToken, uuid.NewString(), "old-decision", detail.ID, moderation.Decision{Decision: "approved", ExpectedVersion: detail.Version})
		if e != command.ErrVersionConflict {
			t.Fatal("旧案件决定未拒绝", e)
		}
	})
	t.Run("危机隔离且普通运营不可读取", func(t *testing.T) {
		draft, event := submit("危机标记不得公开")
		_, e := worker.Consume(ctx, event)
		must(e)
		if state(draft.ID).State != "pending_review" {
			t.Fatal("危机进入公开状态")
		}
		_, e = service.Case(ctx, adminToken, "crisis-detail", event.AggregateID)
		if e != command.ErrNotFound {
			t.Fatal("危机正文泄漏给普通运营", e)
		}
		page, e := service.Cases(ctx, adminToken, "cases", "", 100)
		must(e)
		for _, item := range page.Items {
			if item.ID == event.AggregateID {
				t.Fatal("危机出现在普通队列")
			}
		}
		var count int64
		must(root.Model(&entity.ContentRestriction{}).Where("source_ref IN (SELECT id FROM evidence.crisis_cases WHERE source_task_id=?) AND kind='crisis' AND active", event.AggregateID).Count(&count).Error)
		if count != 1 {
			t.Fatal("危机未持久隔离")
		}
	})
	t.Run("外部失败重试与扫描期间改文", func(t *testing.T) {
		draft, event := submit("需扫描的正文")
		provider.set(true, nil)
		_, e := worker.Consume(ctx, event)
		if e == nil || state(draft.ID).State != "pending_review" {
			t.Fatal("外部失败被放行")
		}
		provider.set(false, func() {
			row := state(draft.ID)
			_, e = wishes.Update(ctx, studentToken, uuid.NewString(), "concurrent-edit", row.ID, wish.UpdateInput{ContentInput: wish.ContentInput{Category: "help", Campus: "wushan", Body: "新正文"}, ExpectedVersion: row.Version})
			if e != nil {
				panic(e)
			}
		})
		must(root.Exec("UPDATE public.work_items SET next_at=clock_timestamp() WHERE id=?", event.CorrelationID).Error)
		out, e := worker.Consume(ctx, event)
		provider.set(false, nil)
		must(e)
		if !out.Cancelled || state(draft.ID).State != "draft" {
			t.Fatal("旧扫描复活新正文")
		}
	})
	t.Run("外部分析不持有业务行锁", func(t *testing.T) {
		draft, event := submit("事务外扫描正文")
		provider.set(false, func() {
			if failure := root.Transaction(func(db *gorm.DB) error {
				if e := db.Exec("SELECT id FROM public.accounts WHERE id=? FOR UPDATE NOWAIT", owner).Error; e != nil {
					return e
				}
				return db.Exec("SELECT id FROM public.wishes WHERE id=? FOR UPDATE NOWAIT", draft.ID).Error
			}); failure != nil {
				panic("外部调用持有业务锁")
			}
		})
		out, e := worker.Consume(ctx, event)
		provider.set(false, nil)
		must(e)
		if !out.Applied {
			t.Fatal("事务外扫描未完成")
		}
	})
	t.Run("审计失败回滚决定和业务结果", func(t *testing.T) {
		draft, event := submit("普通命中审计故障")
		_, e := worker.Consume(ctx, event)
		must(e)
		row := task(event.AggregateID)
		must(root.Exec("CREATE FUNCTION public.reject_moderation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$").Error)
		must(root.Exec("CREATE TRIGGER reject_moderation_audit BEFORE INSERT ON public.access_audits FOR EACH ROW EXECUTE FUNCTION public.reject_moderation_audit()").Error)
		_, e = service.Decide(ctx, adminToken, uuid.NewString(), "audit-failure", row.ID, moderation.Decision{Decision: "approved", ExpectedVersion: row.Version})
		must(root.Exec("DROP TRIGGER reject_moderation_audit ON public.access_audits").Error)
		must(root.Exec("DROP FUNCTION public.reject_moderation_audit()").Error)
		if e == nil || task(row.ID).Version != row.Version || state(draft.ID).State != "pending_review" {
			t.Fatal("审计失败留下成功结果")
		}
	})
	t.Run("学生与撤销角色不能访问后台", func(t *testing.T) {
		_, e := service.Wordlists(ctx, studentToken, "student", "", 20)
		if e != command.ErrUnauthenticated {
			t.Fatal("学生令牌跨域", e)
		}
		must(root.Where("admin_id=?", admin).Delete(&entity.AdminRoleBinding{}).Error)
		_, e = service.Wordlists(ctx, adminToken, "revoked", "", 20)
		if e != command.ErrForbidden {
			t.Fatal("缓存旧角色", e)
		}
		must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "review_operator"}).Error)
	})
	t.Run("并发快照基准和案件决定", func(t *testing.T) {
		h := head("school")
		results := make(chan error, 2)
		var running sync.WaitGroup
		for i := 0; i < 2; i++ {
			running.Add(1)
			go func() {
				defer running.Done()
				_, e := service.CreateWordlist(ctx, adminToken, uuid.NewString(), "concurrent-draft", moderation.WordlistInput{Kind: "school", Entries: []string{"校务标记"}, ExpectedVersion: h.Version})
				results <- e
			}()
		}
		running.Wait()
		close(results)
		passed, conflicts := 0, 0
		for e := range results {
			if e == nil {
				passed++
			} else if e == command.ErrVersionConflict {
				conflicts++
			} else {
				t.Fatal(e)
			}
		}
		if passed != 1 || conflicts != 1 {
			t.Fatal("同一基准并发覆盖")
		}
		_, event := submit("普通命中并发决定")
		_, e := worker.Consume(ctx, event)
		must(e)
		v := task(event.AggregateID).Version
		decisions := make(chan error, 2)
		for i := 0; i < 2; i++ {
			running.Add(1)
			go func() {
				defer running.Done()
				_, e := service.Decide(ctx, adminToken, uuid.NewString(), "concurrent-decision", event.AggregateID, moderation.Decision{Decision: "approved", ExpectedVersion: v})
				decisions <- e
			}()
		}
		running.Wait()
		close(decisions)
		passed, conflicts = 0, 0
		for e := range decisions {
			if e == nil {
				passed++
			} else if e == command.ErrVersionConflict {
				conflicts++
			} else {
				t.Fatal(e)
			}
		}
		if passed != 1 || conflicts != 1 {
			t.Fatal("案件并发决定重复生效")
		}
	})
	t.Run("规则更新只作用后续扫描", func(t *testing.T) {
		_, old := submit("新学校标记旧扫描")
		_, e := worker.Consume(ctx, old)
		must(e)
		before := task(old.AggregateID)
		var prior entity.ModerationScan
		must(root.Where("task_id=?", old.AggregateID).Take(&prior).Error)
		updated := publish("school", []string{"校务标记", "新学校标记"})
		if task(old.AggregateID).Version != before.Version {
			t.Fatal("发布词库自动重扫历史")
		}
		var after entity.ModerationScan
		must(root.Where("task_id=?", old.AggregateID).Take(&after).Error)
		if prior.SchoolVersion != after.SchoolVersion {
			t.Fatal("历史规则引用被替换")
		}
		_, next := submit("新学校标记新扫描")
		_, e = worker.Consume(ctx, next)
		must(e)
		after = entity.ModerationScan{}
		must(root.Where("task_id=?", next.AggregateID).Take(&after).Error)
		if !after.SchoolRelated || after.SchoolVersion != updated.ID || task(next.AggregateID).Status != "human_required" {
			t.Fatal("新扫描未应用新规则")
		}
	})
	t.Run("隐私屏障代次和当前资格复核", func(t *testing.T) {
		draft, event := submit("普通命中屏障")
		_, e := worker.Consume(ctx, event)
		must(e)
		v := task(event.AggregateID).Version
		must(root.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "wish", TargetID: draft.ID, HoldEpoch: 1, RequiredContentVersion: 2, Status: "active"}).Error)
		_, e = service.Decide(ctx, adminToken, uuid.NewString(), "old-hold", event.AggregateID, moderation.Decision{Decision: "approved", ExpectedVersion: v})
		if e != command.ErrVersionConflict {
			t.Fatal("旧屏障案件被放行", e)
		}
		detail, e := service.Case(ctx, adminToken, "held-detail", event.AggregateID)
		if e != command.ErrNotFound || detail.Body != "" {
			t.Fatal("屏障期间解密旧正文", e)
		}
		draft, event = submit("普通命中资格撤销")
		_, e = worker.Consume(ctx, event)
		must(e)
		v = task(event.AggregateID).Version
		must(root.Model(&entity.Account{}).Where("id=?", owner).Update("verification", "unverified").Error)
		outcome, e := service.Decide(ctx, adminToken, uuid.NewString(), "qualification-revoked", event.AggregateID, moderation.Decision{Decision: "approved", ExpectedVersion: v})
		must(e)
		if outcome.ContentDecision != "approved" || outcome.BusinessOutcome != "cancelled" || state(draft.ID).State != "pending_review" {
			t.Fatal("旧资格审核复活业务")
		}
		must(root.Model(&entity.Account{}).Where("id=?", owner).Update("verification", "verified").Error)
	})
	t.Run("数据库补偿已确认丢失的唤醒", func(t *testing.T) {
		draft, event := submit("数据库补偿正文")
		must(root.Model(&entity.OutboxEvent{}).Where("event_id=?", event.EventID).Update("sent_at", time.Now()).Error)
		for i := 0; i < 8 && state(draft.ID).State != "ready"; i++ {
			_, e := worker.ProcessNext(ctx)
			must(e)
		}
		if state(draft.ID).State != "ready" {
			t.Fatal("已确认丢失唤醒未被补偿")
		}
		checkReceipt(event)
	})
	t.Run("真实HTTP授权和正式响应", func(t *testing.T) {
		router := httpapi.New(logger, nil)
		must(httpapi.RegisterModeration(router, service))
		request := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			req.Header.Set("Idempotency-Key", uuid.NewString())
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			return rec
		}
		rec := request("GET", "/api/v1/admin/wordlists?limit=1", adminToken, nil)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("正式列表HTTP失败", rec.Code)
		}
		var envelope struct {
			Data moderation.WordlistPage `json:"data"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &envelope))
		if len(envelope.Data.Heads) != 3 || len(envelope.Data.Items) != 1 || envelope.Data.NextCursor == nil {
			t.Fatal("分页丢失三类基准")
		}
		if got := request("GET", "/api/v1/admin/wordlists", studentToken, nil).Code; got != 401 {
			t.Fatal("HTTP学生会话跨域", got)
		}
		_, event := submit("普通命中HTTP审核")
		_, e := worker.Consume(ctx, event)
		must(e)
		row := task(event.AggregateID)
		body, _ := json.Marshal(moderation.Decision{Decision: "rejected", Reason: "合成原因", RevisionSuggestion: "合成建议", ExpectedVersion: row.Version})
		rec = request("POST", "/api/v1/admin/moderation-tasks/"+row.ID+"/decision", adminToken, body)
		if rec.Code != 200 {
			t.Fatal("正式审核HTTP失败", rec.Code)
		}
	})

	// 下列生命周期用例使用同一真实运行权限与持久任务；供应商保持合成。
	t.Run("数据库补偿分析预算独立", func(t *testing.T) {
		// 隔离本用例待处理目标，避免历史失败夹具占用有界的一条候选。
		must(root.Model(&entity.WorkItem{}).Where("kind=? AND status IN ?", wish.ReviewRoute().Kind, []string{"pending", "retry_wait", "leased"}).Update("next_at", time.Now().Add(time.Hour)).Error)
		draft, event := submit("较慢合成分析")
		provider.set(false, func() { time.Sleep(700 * time.Millisecond) })
		defer provider.set(false, nil)
		slow, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{AnalysisTimeout: 2 * time.Second, Consumer: durable.ConsumerConfig{Timeout: 400 * time.Millisecond, LockTimeout: 100 * time.Millisecond, RetryDelay: time.Millisecond, MaxAttempts: 3}})
		must(e)
		out, e := slow.ProcessNext(ctx)
		must(e)
		if !out.Applied || state(draft.ID).State != "ready" {
			t.Fatal("查询预算提前取消了独立分析")
		}
		checkReceipt(event)
	})
	t.Run("常驻补偿关闭与重启恢复", func(t *testing.T) {
		draft, event := submit("后台补偿合成正文")
		must(root.Model(&entity.OutboxEvent{}).Where("event_id=?", event.EventID).Update("sent_at", time.Now()).Error)
		run, e := workrunner.OpenDatabase(ctx, worker, logger, 10*time.Millisecond, 30*time.Second)
		must(e)
		// 即使最终断言失败也先关闭调度，避免释放池后仍有在途操作。
		defer run.Close()
		deadline := time.Now().Add(5 * time.Second)
		for state(draft.ID).State != "ready" && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		must(run.Close())
		if state(draft.ID).State != "ready" {
			t.Fatal("常驻进程未补偿已确认的唤醒")
		}
		checkReceipt(event)
		draft, event = submit("重启恢复合成正文")
		time.Sleep(30 * time.Millisecond)
		if state(draft.ID).State != "pending_review" {
			t.Fatal("关闭后仍处理新工作")
		}
		run, e = workrunner.OpenDatabase(ctx, worker, logger, 10*time.Millisecond, 30*time.Second)
		must(e)
		defer run.Close()
		deadline = time.Now().Add(5 * time.Second)
		for state(draft.ID).State != "ready" && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		must(run.Close())
		if state(draft.ID).State != "ready" {
			t.Fatal("重启未恢复数据库待办")
		}
		checkReceipt(event)
	})
	if nameserver := os.Getenv("APP_MODERATION_TEST_NAMESERVER"); nameserver != "" {
		t.Run("真实MQ提交后NACK重投", func(t *testing.T) {
			_, event := submit("普通命中消息往返")
			unique := "review_" + uuid.NewString()[:8]
			cfg := transportmq.Config{NameServers: []string{nameserver}, Topic: "liyuanci_content_review_v1", EventType: wish.ReviewRoute().EventType, ProducerGroup: unique + "_p", ConsumerGroup: unique + "_c", Instance: unique, AllowUnauthenticated: true, SendTimeout: 3 * time.Second, HandlerTimeout: 15 * time.Second, Concurrency: 1, MaxReconsume: 3, RetryDelayLevel: 1}
			must(transportmq.ConfigureSDKLogging(logger))
			h := &handler{worker: worker, seen: make(chan durable.Consumption, 128)}
			sub, e := transportmq.OpenSubscriber(ctx, cfg, h, logger)
			must(e)
			defer sub.Close()
			sender, e := transportmq.OpenSender(ctx, cfg, logger)
			must(e)
			defer sender.Close()
			must(sender.Send(ctx, event))
			deadline := time.NewTimer(70 * time.Second)
			defer deadline.Stop()
			applied, replayed := false, false
			for !applied || !replayed {
				select {
				case out := <-h.seen:
					applied = applied || out.Applied
					replayed = replayed || out.Replayed
				case <-deadline.C:
					t.Fatal("真实MQ重投超时")
				}
			}
			checkReceipt(event)
		})
		t.Run("常驻消息转发与内容订阅", func(t *testing.T) {
			unique := "runtime_" + uuid.NewString()[:8]
			cfg := workrunner.Config{
				MQ:            transportmq.Config{NameServers: []string{nameserver}, Topic: "liyuanci_content_review_v1", EventType: wish.ReviewRoute().EventType, ProducerGroup: unique + "_p", ConsumerGroup: unique + "_c", Instance: unique, AllowUnauthenticated: true, SendTimeout: 3 * time.Second, HandlerTimeout: 30 * time.Second, Concurrency: 1, MaxReconsume: 3, RetryDelayLevel: 1},
				Relay:         durable.Config{DatabaseTimeout: 3 * time.Second, SendTimeout: 3 * time.Second, LeaseDuration: 15 * time.Second, RetryDelay: time.Second, MaxAttempts: 5},
				RelayInterval: 10 * time.Millisecond, ScanInterval: time.Hour, OperationTimeout: 30 * time.Second,
			}
			// 先让补偿首轮完成，再提交新任务，使本目标只能经真实MQ消费或测试超时。
			run, e := workrunner.Open(ctx, workerDB, worker, logger, cfg, wish.ReviewRoute())
			must(e)
			defer run.Close()
			time.Sleep(100 * time.Millisecond)
			draft, event := submit("常驻消息合成正文")
			deadline := time.Now().Add(20 * time.Second)
			for state(draft.ID).State != "ready" && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			must(run.Close())
			if state(draft.ID).State != "ready" {
				t.Fatal("常驻消息链未完成审核")
			}
			var row entity.OutboxEvent
			must(root.Where("event_id=?", event.EventID).Take(&row).Error)
			if row.SentAt == nil {
				t.Fatal("领域结果缺消息转发确认")
			}
			checkReceipt(event)
		})

	}
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, "SYNTHETIC_PROVIDER_SECRET") || strings.Contains(entry.Message, "合成个人姓名") {
			t.Fatal("敏感数据进入日志")
		}
	}
	if !t.Failed() {
		t.Log("真实PG权限/事务/增量迁移、完整词库双版本、心愿扫描/人工复核及实际MQ往返通过；安全分析供应商为合成夹具")
	}
}
