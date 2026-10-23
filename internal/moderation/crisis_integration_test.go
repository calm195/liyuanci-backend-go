package moderation_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// crisisNotifier 只记录稳定投递引用；失败不发送正文和身份。
type crisisNotifier struct {
	mu   sync.Mutex
	fail bool
	ids  map[string]int
	hook func()
}

func (n *crisisNotifier) NotifyCrisis(ctx context.Context, id, caseID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.hook != nil {
		n.hook()
	}
	if n.fail {
		return errors.New("SYNTHETIC_NOTIFY_SECRET")
	}
	if n.ids == nil {
		n.ids = map[string]int{}
	}
	n.ids[id]++
	return ctx.Err()
}

// TestCrisisCare 验证T029/REQ-015/017/018/019的真实数据库、HTTP、持久待办与误报路径；所有资料和供应商合成。
func TestCrisisCare(t *testing.T) {
	dsn := os.Getenv("APP_MODERATION_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PostgreSQL，使用make integration执行")
	}
	ctx := context.Background()
	logger := zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	open := func(value string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		must(e)
		pool, e := db.DB()
		must(e)
		t.Cleanup(func() { pool.Close() })
		return db
	}
	parent := open(dsn)
	database := "crisis_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if !strings.Contains(dsn, "dbname=liyuanci ") {
		t.Fatal("需要隔离集成DSN")
	}
	must(parent.Exec("CREATE DATABASE " + database).Error)
	dsn = strings.Replace(dsn, "dbname=liyuanci ", "dbname="+database+" ", 1)
	root := open(dsn)
	must(migration.Run(ctx, root, logger, "up"))
	must(migration.ApplyPermissions(ctx, root, logger))
	must(migration.Run(ctx, root, logger, "down"))
	must(migration.Run(ctx, root, logger, "up"))
	must(migration.ApplyPermissions(ctx, root, logger))
	api, adminDB, workerDB := open(dsn+" options='-c role=liyuanci_api'"), open(dsn+" options='-c role=liyuanci_admin_api'"), open(dsn+" options='-c role=liyuanci_domain_worker'")
	codec, e := securedata.New(testKeys{}, logger)
	must(e)
	cfg := command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 5}
	wishes, e := wish.NewService(api, logger, codec, nil, wish.ServiceConfig{MaxStoryBytes: 65536, Command: cfg, ReceiptKey: []byte(strings.Repeat("a", 32))})
	must(e)
	reviews, e := moderation.New(adminDB, logger, codec, moderation.Config{Command: cfg, CursorKey: []byte(strings.Repeat("b", 32)), MaxEntries: 100})
	must(e)
	provider := &analyzer{}
	worker, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{MessageAnalyzer: privateAnalyzer{}, AnalysisTimeout: 5 * time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}})
	must(e)
	admin, adminToken := uuid.NewString(), uuid.NewString()
	credential := "synthetic-application-reviewer"
	must(root.Create(&entity.AdminAccount{ID: admin, CredentialRef: &credential}).Error)
	must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "review_operator"}).Error)
	hash := sha256.Sum256([]byte(adminToken))
	must(root.Create(&entity.Session{ID: uuid.NewString(), AdminID: &admin, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
	for _, item := range []struct{ kind, word string }{{"ordinary_violation", "普通命中"}, {"school", "学校命中"}, {"crisis", "危机命中"}} {
		heads, e := reviews.Wordlists(ctx, adminToken, "rules", "", 100)
		must(e)
		var v int64
		for _, h := range heads.Heads {
			if h.Kind == item.kind {
				v = h.Version
			}
		}
		draft, e := reviews.CreateWordlist(ctx, adminToken, uuid.NewString(), "rules", moderation.WordlistInput{Kind: item.kind, Entries: []string{item.word}, ExpectedVersion: v})
		must(e)
		heads, e = reviews.Wordlists(ctx, adminToken, "rules", "", 100)
		must(e)
		for _, h := range heads.Heads {
			if h.Kind == item.kind {
				v = h.Version
			}
		}
		_, e = reviews.PublishWordlist(ctx, adminToken, uuid.NewString(), "rules", draft.ID, moderation.WordlistPublish{ExpectedVersion: draft.Version, ExpectedHeadVersion: v})
		must(e)
	}
	student := func() (string, string) {
		t.Helper()
		id, token := uuid.NewString(), uuid.NewString()
		must(root.Create(&entity.Account{ID: id, Nickname: "合成参与人", Verification: "verified"}).Error)
		h := sha256.Sum256([]byte(token))
		must(root.Create(&entity.Session{ID: uuid.NewString(), AccountID: &id, TokenHash: h[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
		return id, token
	}
	event := func(kind, id string) durable.Event {
		t.Helper()
		var out durable.Event
		must(root.Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.kind=? AND c.entity_id=? AND e.superseded_at IS NULL ORDER BY t.created_at DESC LIMIT 1`, kind, id).Scan(&out).Error)
		if out.EventID == "" {
			t.Fatal("未原子创建审核工作")
		}
		return out
	}
	scan := func(kind, id string) {
		t.Helper()
		out, e := worker.Consume(ctx, event(kind, id))
		must(e)
		if !out.Applied {
			t.Fatal("扫描未应用")
		}
	}
	published := func(token string) wish.CreatedDraft {
		t.Helper()
		out, e := wishes.Create(ctx, token, uuid.NewString(), "create", wish.ContentInput{Category: "help", Campus: "wushan", Body: "合成招募心愿"})
		must(e)
		out, e = wishes.Submit(ctx, token, uuid.NewString(), "submit", out.ID, out.Version, out.ContentVersion)
		must(e)
		scan("wish", out.ID)
		var row entity.Wish
		must(root.Where("id=?", out.ID).Take(&row).Error)
		out, e = wishes.Publish(ctx, token, uuid.NewString(), "publish", row.ID, row.Version)
		must(e)
		if out.State != wish.Published {
			t.Fatal("夹具未能上架")
		}
		return out
	}
	crisisDB := open(dsn + " options='-c role=liyuanci_crisis_api'")
	crisis, e := moderation.New(crisisDB, logger, codec, moderation.Config{Command: cfg, CursorKey: []byte(strings.Repeat("d", 32)), MaxEntries: 100})
	must(e)
	operator, operatorToken := uuid.NewString(), uuid.NewString()
	crisisCredential := "synthetic-crisis-operator"
	must(root.Create(&entity.AdminAccount{ID: operator, CredentialRef: &crisisCredential}).Error)
	must(root.Create(&entity.AdminRoleBinding{AdminID: operator, Role: "crisis_operator"}).Error)
	h := sha256.Sum256([]byte(operatorToken))
	must(root.Create(&entity.Session{ID: uuid.NewString(), AdminID: &operator, TokenHash: h[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
	notifier := &crisisNotifier{fail: true}
	resources := &moderation.CareResources{Version: "synthetic-v1", Source: "synthetic://test-only", VerifiedAt: "2026-10-23", Hotline24Hours: "合成测试渠道，无真实号码", Appointment: "合成预约入口", Notice: "合成资料，仅隔离验收"}
	makeWorker := func(care *moderation.CareResources) *moderation.Worker {
		t.Helper()
		w, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{Care: care, CrisisNotifier: notifier, MessageAnalyzer: privateAnalyzer{}, AnalysisTimeout: time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Minute, MaxAttempts: 3}})
		must(e)
		return w
	}
	worker = makeWorker(nil)
	owner, token := student()
	identity, e := codec.Encrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: owner, Version: 1}, []byte("SYNTHETIC_STUDENT_NUMBER"))
	must(e)
	raw, e := json.Marshal(identity)
	must(e)
	must(root.Create(&entity.IdentityLookupKey{Purpose: "student_number", CurrentRef: "synthetic-lookup", Version: 1}).Error)
	must(root.Create(&entity.IdentitySecret{AccountID: owner, Purpose: "student_number", ContentVersion: 1, Envelope: raw}).Error)
	create := func(body string) wish.CreatedDraft {
		t.Helper()
		w, e := wishes.Create(ctx, token, uuid.NewString(), "create", wish.ContentInput{Category: "help", Campus: "wushan", Body: body})
		must(e)
		w, e = wishes.Submit(ctx, token, uuid.NewString(), "submit", w.ID, w.Version, w.ContentVersion)
		must(e)
		return w
	}
	findCase := func(id string) entity.EvidenceCrisisCase {
		t.Helper()
		var c entity.EvidenceCrisisCase
		must(root.Table("evidence.crisis_cases AS c").Select("c.*").Joins("JOIN evidence.evidence_cases ec ON ec.id=c.evidence_case_id JOIN public.content_registry cr ON cr.id=ec.source_ref").Where("cr.entity_id=?", id).Take(&c).Error)
		return c
	}
	count := func(table, where string, args ...any) int64 {
		t.Helper()
		var n int64
		must(root.Table(table).Where(where, args...).Count(&n).Error)
		return n
	}
	makeDue := func() {
		must(root.Exec("UPDATE evidence.crisis_deliveries SET next_at=clock_timestamp()-interval '1 second',lease_until=clock_timestamp()-interval '1 second' WHERE status IN ('pending','leased')").Error)
	}
	w := create("危机命中 合成个人姓名")
	scan("wish", w.ID)
	c := findCase(w.ID)
	t.Run("隔离证据原子持久与消费重放", func(t *testing.T) {
		if count("evidence.evidence_items", "case_id=?", c.EvidenceCaseID) != 1 || count("evidence.crisis_deliveries", "case_id=?", c.ID) != 2 {
			t.Fatal("证据或双待办缺失")
		}
		out, e := worker.Consume(ctx, event("wish", w.ID))
		must(e)
		if !out.Replayed {
			t.Fatal("消费未去重")
		}
		var item entity.EvidenceItem
		must(root.Where("case_id=?", c.EvidenceCaseID).Take(&item).Error)
		if strings.Contains(string(item.SubjectCipher), "SYNTHETIC_STUDENT_NUMBER") || strings.Contains(string(item.BodyCipher), "危机命中") {
			t.Fatal("证据明文落库")
		}
		if item.AssetRef != nil || count("evidence.assets", "true") != 0 {
			t.Fatal("无关学生卡进入证据")
		}
		var current entity.Wish
		must(root.Where("id=?", w.ID).Take(&current).Error)
		_, e = wishes.Publish(ctx, token, uuid.NewString(), "publish", w.ID, current.Version)
		if e == nil {
			t.Fatal("隔离内容上架")
		}
	})
	t.Run("具体案件角色权限及访问留迹", func(t *testing.T) {
		_, e := crisis.CrisisCase(ctx, adminToken, "ordinary", c.ID)
		if !errors.Is(e, command.ErrForbidden) {
			t.Fatal(e)
		}
		_, e = crisis.CrisisCase(ctx, token, "student", c.ID)
		if !errors.Is(e, command.ErrUnauthenticated) {
			t.Fatal(e)
		}
		detail, e := crisis.CrisisCase(ctx, operatorToken, "authorized", c.ID)
		must(e)
		if detail.StudentNumber == nil || *detail.StudentNumber != "SYNTHETIC_STUDENT_NUMBER" || detail.Body == nil || *detail.Body != "危机命中 合成个人姓名" {
			t.Fatal("限定证据投影错误")
		}
		if count("evidence.evidence_actions", "case_id=? AND action='view'", c.EvidenceCaseID) != 1 {
			t.Fatal("访问未留迹")
		}
		item, e := crisis.CrisisEvidence(ctx, operatorToken, "evidence", c.EvidenceCaseID)
		must(e)
		if item.CaseID != c.ID {
			t.Fatal("证据跨案")
		}
		list, e := crisis.CrisisCases(ctx, operatorToken, "queue", "", 1)
		must(e)
		if len(list.Items) != 1 || list.Items[0].Body != nil || list.Items[0].StudentNumber != nil {
			t.Fatal("列表泄漏")
		}
		_, e = reviews.Case(ctx, adminToken, "ordinary", *c.SourceTaskID)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal("普通审核读取危机", e)
		}
	})
	t.Run("缺依赖及失败不丢待办和隔离", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			_, e := worker.ProcessCrisisDelivery(ctx)
			if e == nil {
				t.Fatal("缺依赖未报告失败")
			}
		}
		if count("evidence.crisis_deliveries", "case_id=? AND status='pending' AND attempts=1", c.ID) != 2 {
			t.Fatal("待办未保留")
		}
		if count("public.content_restrictions", "source_ref=? AND active", c.ID) != 1 {
			t.Fatal("失败恢复公开")
		}
	})
	t.Run("重启及租约过期补偿与关怀本人投影", func(t *testing.T) {
		worker = makeWorker(resources)
		notifier.fail = false
		makeDue()
		must(root.Exec("UPDATE evidence.crisis_deliveries SET status='leased',lease_id=?,lease_until=clock_timestamp()-interval '1 second' WHERE case_id=? AND kind='operator'", uuid.NewString(), c.ID).Error)
		for i := 0; i < 2; i++ {
			_, e := worker.ProcessCrisisDelivery(ctx)
			must(e)
		}
		if count("evidence.crisis_deliveries", "case_id=? AND status='sent'", c.ID) != 2 {
			t.Fatal("补偿未完成")
		}
		chats, e := chat.New(api, logger, codec, chat.Config{Command: cfg, Key: []byte(strings.Repeat("c", 32)), MaxBodyBytes: 65536})
		must(e)
		page, e := chats.Notifications(ctx, token, "care", chat.Query{Limit: 50})
		must(e)
		seen := false
		for _, n := range page.Items {
			if n.Kind == "crisis_care" {
				seen = true
				if n.TargetRef == nil {
					t.Fatal("关怀缺少安全引用")
				}
				card, e := chats.Care(ctx, token, "care", *n.TargetRef)
				must(e)
				if len(card.Resources) == 0 {
					t.Fatal("援助资料缺失")
				}

			}
		}
		if !seen {
			t.Fatal("关怀未送达")
		}
		_, other := student()
		page, e = chats.Notifications(ctx, other, "other", chat.Query{Limit: 50})
		must(e)
		if len(page.Items) != 0 {
			t.Fatal("跨账号关怀")
		}
		if len(notifier.ids) != 1 {
			t.Fatal("通知未使用稳定投递引用")
		}
	})
	t.Run("幂等版本及误报新任务不直接公开", func(t *testing.T) {
		input := moderation.CrisisCommand{Action: "take_over", Reason: "合成人工接手", ExpectedVersion: c.Version}
		key := uuid.NewString()
		out, e := crisis.ActOnCrisis(ctx, operatorToken, key, "take", c.ID, input, false)
		must(e)
		replay, e := crisis.ActOnCrisis(ctx, operatorToken, key, "replay", c.ID, input, false)
		must(e)
		if replay.Version != out.Version {
			t.Fatal("重放重复动作")
		}
		input.Reason = "异请求"
		_, e = crisis.ActOnCrisis(ctx, operatorToken, key, "conflict", c.ID, input, false)
		if !errors.Is(e, command.ErrIdempotencyConflict) {
			t.Fatal(e)
		}
		_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "stale", c.ID, moderation.CrisisCommand{Action: "follow_up", Reason: "合成", ExpectedVersion: 1}, false)
		if !errors.Is(e, command.ErrVersionConflict) {
			t.Fatal(e)
		}
		out, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false_positive", c.ID, moderation.CrisisCommand{Reason: "合成误报复核", ExpectedVersion: out.Version}, true)
		must(e)
		if out.Status != "false_positive" {
			t.Fatal("未解除误报")
		}
		var current entity.Wish
		must(root.Where("id=?", w.ID).Take(&current).Error)
		if current.State != "pending_review" {
			t.Fatal("误报自动公开")
		}
		var v entity.WishVersion
		must(root.Where("wish_id=? AND content_version=1", w.ID).Take(&v).Error)
		if v.ReviewTaskID == nil || *v.ReviewTaskID == *c.SourceTaskID || v.PublicBody != nil {
			t.Fatal("复用了旧危机任务或恢复正文")
		}
		task, e := reviews.Case(ctx, adminToken, "ordinary", *v.ReviewTaskID)
		must(e)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "approve", task.ID, moderation.Decision{Decision: "approved", ExpectedVersion: task.Version})
		must(e)
		must(root.Where("id=?", w.ID).Take(&current).Error)
		if current.State != "ready" {
			t.Fatal("普通复核未到待上架")
		}
	})
	t.Run("已公开巡查与举报升级立即隐藏", func(t *testing.T) {
		pub := published(token)
		ev := event("wish", pub.ID)
		var task entity.ModerationTask
		must(root.Where("id=?", ev.AggregateID).Take(&task).Error)
		out, e := crisis.EscalateCrisis(ctx, operatorToken, uuid.NewString(), "patrol", "task", task.ID, moderation.CrisisCommand{Reason: "合成巡查", ExpectedVersion: task.Version})
		must(e)
		var current entity.Wish
		must(root.Where("id=?", pub.ID).Take(&current).Error)
		if current.State != "withdrawn" {
			t.Fatal("已公开危机未下线")
		}
		_, e = reviews.Case(ctx, adminToken, "ordinary", task.ID)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal("巡查危机泄漏普通审", e)
		}
		if out.ID == c.ID {
			t.Fatal("跨内容合并案件")
		}
		pub2 := published(token)
		reporter, _ := student()
		var registry entity.ContentRegistry
		must(root.Where("kind='wish' AND entity_id=? AND content_version=?", pub2.ID, pub2.ContentVersion).Take(&registry).Error)
		report := entity.Report{ID: uuid.NewString(), ReporterID: reporter, TargetKind: "wish", TargetID: pub2.ID, TypeCode: "synthetic-crisis", ContentID: &registry.ID, Version: 1}
		must(root.Create(&report).Error)
		escalated, e := crisis.EscalateCrisis(ctx, operatorToken, uuid.NewString(), "report-escalation", "report", report.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成举报风险复核"})
		must(e)
		if escalated.ID == out.ID {
			t.Fatal("举报升级串案")
		}

	})
	t.Run("HTTP严格校验角色与无涉案资产", func(t *testing.T) {
		router := httpapi.New(logger, func(context.Context) error { return nil })
		must(httpapi.RegisterCrisis(router, crisis))
		for _, tc := range []struct {
			method, path, token, body string
			status                    int
		}{
			{"GET", "/admin/crisis-cases", adminToken, "", 403}, {"GET", "/admin/crisis-cases/" + c.ID, operatorToken, "", 200},
			{"GET", "/admin/evidence/" + c.EvidenceCaseID + "/asset", operatorToken, "", 404},
			{"POST", "/admin/crisis-cases/" + c.ID + "/actions", operatorToken, `{"action":"resolve","reason":"合成","expected_version":1,"student_number":"forged"}`, 400},
			{"GET", "/admin/crisis-cases?limit=1&limit=2", operatorToken, "", 400},
		} {
			req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", uuid.NewString())
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatal(fmt.Sprintf("%s: %d %s", tc.path, rr.Code, rr.Body.String()))
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("敏感响应可缓存")
			}
		}
	})
	t.Run("证据落库失败整单回滚再重试", func(t *testing.T) {
		w := create("危机命中 原子回滚")
		must(root.Exec(`CREATE FUNCTION public.reject_crisis_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic evidence failure'; END $$; CREATE TRIGGER reject_crisis_evidence BEFORE INSERT ON evidence.evidence_items FOR EACH ROW EXECUTE FUNCTION public.reject_crisis_evidence()`).Error)
		out, _ := worker.Consume(ctx, event("wish", w.ID))
		if out.Applied {
			t.Fatal("证据失败仍提交扫描")
		}
		if count("evidence.evidence_cases", "source_ref IN (SELECT id FROM public.content_registry WHERE entity_id=?)", w.ID) != 0 {
			t.Fatal("失败留下半案")
		}
		if count("public.content_restrictions", "content_id IN (SELECT id FROM public.content_registry WHERE entity_id=?)", w.ID) != 0 {
			t.Fatal("失败留下半隔离")
		}
		must(root.Exec(`DROP TRIGGER reject_crisis_evidence ON evidence.evidence_items;DROP FUNCTION public.reject_crisis_evidence()`).Error)
		ev := event("wish", w.ID)
		must(root.Exec("UPDATE public.work_items SET next_at=clock_timestamp() WHERE id=?", ev.CorrelationID).Error)
		scan("wish", w.ID)
		if findCase(w.ID).ID == "" {
			t.Fatal("重试未建案")
		}
	})
	t.Run("并发接手唯一版本与撤销角色重放拒绝", func(t *testing.T) {
		w := create("危机命中 并发接手")
		scan("wish", w.ID)
		target := findCase(w.ID)
		key := uuid.NewString()
		in := moderation.CrisisCommand{Action: "take_over", Reason: "合成并发接手", ExpectedVersion: 1}
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				_, e := crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "race", target.ID, in, false)
				results <- e
			}()
		}
		successes, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			e := <-results
			if e == nil {
				successes++
			} else if errors.Is(e, command.ErrVersionConflict) {
				conflicts++
			} else {
				t.Fatal(e)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatal(successes, conflicts)
		}
		in.ExpectedVersion = 2
		in.Action = "follow_up"
		_, e := crisis.ActOnCrisis(ctx, operatorToken, key, "follow", target.ID, in, false)
		must(e)
		must(root.Exec("DELETE FROM public.admin_role_bindings WHERE admin_id=? AND role='crisis_operator'", operator).Error)
		_, e = crisis.ActOnCrisis(ctx, operatorToken, key, "revoked-replay", target.ID, in, false)
		if !errors.Is(e, command.ErrForbidden) {
			t.Fatal("角色撤销后重放仍通过", e)
		}
		must(root.Create(&entity.AdminRoleBinding{AdminID: operator, Role: "crisis_operator"}).Error)
	})
	t.Run("通知在途误报取消拒绝晚到恢复待办", func(t *testing.T) {
		w := create("危机命中 晚到通知")
		scan("wish", w.ID)
		target := findCase(w.ID)
		must(root.Exec("UPDATE evidence.crisis_deliveries SET next_at=clock_timestamp()+interval '1 hour' WHERE status='pending'").Error)
		must(root.Exec("UPDATE evidence.crisis_deliveries SET next_at=clock_timestamp()-interval '1 second' WHERE case_id=? AND kind='operator'", target.ID).Error)
		notifier.fail = true
		var actionErr error
		notifier.hook = func() {
			_, actionErr = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "late-notify", target.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成误报"}, true)
		}
		_, e := worker.ProcessCrisisDelivery(ctx)
		if e == nil {
			t.Fatal("通知失败未报告")
		}
		must(actionErr)
		notifier.hook = nil
		notifier.fail = false
		if count("evidence.crisis_deliveries", "case_id=? AND status='cancelled'", target.ID) != 2 {
			t.Fatal("晚到失败结果恢复了取消待办")
		}
	})
	t.Run("注销状态不重建普通内容但限定证据仍可读", func(t *testing.T) {
		id, tok := student()
		draft, e := wishes.Create(ctx, tok, uuid.NewString(), "create", wish.ContentInput{Category: "help", Campus: "wushan", Body: "危机命中 注销边界"})
		must(e)
		draft, e = wishes.Submit(ctx, tok, uuid.NewString(), "submit", draft.ID, draft.Version, draft.ContentVersion)
		must(e)
		scan("wish", draft.ID)
		target := findCase(draft.ID)
		before := count("public.moderation_tasks", "content_id IN (SELECT id FROM public.content_registry WHERE entity_id=?)", draft.ID)
		must(root.Exec("UPDATE public.accounts SET lifecycle='deleting',auth_epoch=auth_epoch+1 WHERE id=?", id).Error)
		_, e = crisis.CrisisCase(ctx, operatorToken, "evidence-after-deleting", target.ID)
		must(e)
		_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false-after-deleting", target.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成误报"}, true)
		must(e)
		if count("public.moderation_tasks", "content_id IN (SELECT id FROM public.content_registry WHERE entity_id=?)", draft.ID) != before {
			t.Fatal("注销重建审核任务")
		}
	})

	t.Run("应愿私聊故事危机与已送达重审保留序号", func(t *testing.T) {
		_, own := student()
		_, helper := student()
		pub := published(own)
		app, e := wishes.Apply(ctx, helper, uuid.NewString(), "apply", pub.ID, wish.ApplicationInput{Message: "危机命中 应愿留言", WishContentVersion: pub.ContentVersion})
		must(e)
		scan("application", app.ID)
		appCase := findCase(app.ID)
		_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false-application", appCase.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成留言误报"}, true)
		must(e)
		var a entity.Application
		must(root.Where("id=?", app.ID).Take(&a).Error)
		var task entity.ModerationTask
		must(root.Where("id=?", *a.ReviewTaskID).Take(&task).Error)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "review-app", task.ID, moderation.Decision{ExpectedVersion: task.Version, Decision: "approved"})
		must(e)
		must(root.Where("id=?", app.ID).Take(&a).Error)
		var current entity.Wish
		must(root.Where("id=?", pub.ID).Take(&current).Error)
		match, e := wishes.AcceptApplication(ctx, own, uuid.NewString(), "accept", a.ID, wish.AcceptInput{ExpectedVersion: a.Version, WishExpectedVersion: current.Version})
		must(e)
		chats, e := chat.New(api, logger, codec, chat.Config{Command: cfg, Key: []byte(strings.Repeat("c", 32)), MaxBodyBytes: 65536})
		must(e)

		pending, e := chats.Send(ctx, own, uuid.NewString(), "crisis-message", match.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "危机命中 未送达私聊", ExpectedWriteEpoch: 1})
		must(e)
		scan("message", pending.MessageID)
		pendingCase := findCase(pending.MessageID)
		var pendingMessage entity.Message
		must(root.Where("id=?", pending.MessageID).Take(&pendingMessage).Error)
		if pendingMessage.DeliverySeq != nil {
			t.Fatal("危机消息提前分配送达序号")
		}
		_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false-pending-message", pendingCase.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成未送达消息误报"}, true)
		must(e)
		must(root.Where("id=?", pending.MessageID).Take(&pendingMessage).Error)
		pendingTask := entity.ModerationTask{}
		must(root.Where("id=?", *pendingMessage.ReviewTaskID).Take(&pendingTask).Error)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "review-pending-message", pendingTask.ID, moderation.Decision{ExpectedVersion: pendingTask.Version, Decision: "rejected", Reason: "合成测试驳回", RevisionSuggestion: "合成修改建议"})
		must(e)
		msg, e := chats.Send(ctx, own, uuid.NewString(), "send", match.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "合成已送达正文", ExpectedWriteEpoch: 1})
		must(e)
		scan("message", msg.MessageID)
		var m entity.Message
		must(root.Where("id=?", msg.MessageID).Take(&m).Error)
		if m.DeliverySeq == nil {
			t.Fatal("夹具未送达")
		}
		seq := *m.DeliverySeq
		task = entity.ModerationTask{}
		must(root.Where("id=?", *m.ReviewTaskID).Take(&task).Error)
		msgCase, e := crisis.EscalateCrisis(ctx, operatorToken, uuid.NewString(), "patrol-message", "task", task.ID, moderation.CrisisCommand{ExpectedVersion: task.Version, Reason: "合成消息巡查"})
		must(e)
		zero := int64(0)
		page, e := chats.Messages(ctx, helper, "isolated", match.ConversationID, chat.Query{AfterSeq: &zero, Limit: 50})
		must(e)
		if len(page.Items) != 0 {
			t.Fatal("已送达危机正文仍可见")
		}
		_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false-message", msgCase.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成消息误报"}, true)
		must(e)
		must(root.Where("id=?", msg.MessageID).Take(&m).Error)
		task = entity.ModerationTask{}
		must(root.Where("id=?", *m.ReviewTaskID).Take(&task).Error)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "review-message", task.ID, moderation.Decision{ExpectedVersion: task.Version, Decision: "approved"})
		must(e)
		must(root.Where("id=?", msg.MessageID).Take(&m).Error)
		if m.DeliverySeq == nil || *m.DeliverySeq != seq || m.Status != "delivered" {
			t.Fatal("已送达重审改变序号", m.Status)
		}

		rejectedMessage, e := chats.Send(ctx, own, uuid.NewString(), "send-to-recheck-reject", match.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "合成已送达后重审驳回", ExpectedWriteEpoch: 1})
		must(e)
		scan("message", rejectedMessage.MessageID)
		var rejectedRow entity.Message
		must(root.Where("id=?", rejectedMessage.MessageID).Take(&rejectedRow).Error)
		rejectedSeq := *rejectedRow.DeliverySeq
		task = entity.ModerationTask{}
		must(root.Where("id=?", *rejectedRow.ReviewTaskID).Take(&task).Error)
		rejectedCase, e := crisis.EscalateCrisis(ctx, operatorToken, uuid.NewString(), "patrol-recheck-reject", "task", task.ID, moderation.CrisisCommand{ExpectedVersion: task.Version, Reason: "合成巡查"})
		must(e)
		_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false-recheck-reject", rejectedCase.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成误报"}, true)
		must(e)
		must(root.Where("id=?", rejectedMessage.MessageID).Take(&rejectedRow).Error)
		task = entity.ModerationTask{}
		must(root.Where("id=?", *rejectedRow.ReviewTaskID).Take(&task).Error)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "reject-recheck", task.ID, moderation.Decision{ExpectedVersion: task.Version, Decision: "rejected", Reason: "合成普通违规", RevisionSuggestion: "合成修改建议"})
		must(e)
		must(root.Where("id=?", rejectedMessage.MessageID).Take(&rejectedRow).Error)
		if rejectedRow.DeliverySeq == nil || *rejectedRow.DeliverySeq != rejectedSeq || rejectedRow.Status != "delivered" {
			t.Fatal("驳回复核破坏历史送达约束")
		}
		page, e = chats.Messages(ctx, helper, "after-recheck-reject", match.ConversationID, chat.Query{AfterSeq: &zero, Limit: 50})
		must(e)
		for _, item := range page.Items {
			if item.MessageID == rejectedMessage.MessageID {
				t.Fatal("普通复核驳回后仍读到旧正文")
			}
		}

		cancelledMessage, e := chats.Send(ctx, own, uuid.NewString(), "send-cancel-before-crisis", match.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "普通命中 业务取消不得升格", ExpectedWriteEpoch: 1})
		must(e)
		scan("message", cancelledMessage.MessageID)
		must(root.Where("id=?", pub.ID).Take(&current).Error)
		_, e = wishes.Complete(ctx, own, uuid.NewString(), "complete", pub.ID, current.Version)
		must(e)

		var cancelledRow entity.Message
		must(root.Where("id=?", cancelledMessage.MessageID).Take(&cancelledRow).Error)
		cancelledTask := entity.ModerationTask{}
		must(root.Where("id=?", *cancelledRow.ReviewTaskID).Take(&cancelledTask).Error)
		_, e = crisis.EscalateCrisis(ctx, operatorToken, uuid.NewString(), "reject-cancelled-evidence", "task", cancelledTask.ID, moderation.CrisisCommand{ExpectedVersion: cancelledTask.Version, Reason: "不能升格的取消任务"})
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal("取消消息被升级为永久危机证据", e)
		}
		if count("evidence.evidence_cases", "source_ref IN (SELECT id FROM public.content_registry WHERE entity_id=?)", cancelledMessage.MessageID) != 0 {
			t.Fatal("取消消息留下永久证据")
		}
		must(root.Where("id=?", pub.ID).Take(&current).Error)
		_, e = wishes.SubmitStory(ctx, own, uuid.NewString(), "story", pub.ID, wish.StoryInput{Body: "危机命中 故事", WishExpectedVersion: current.Version})
		must(e)
		scan("story", pub.ID)
		storyCase := findCase(pub.ID)
		if storyCase.ContentType != "story" {
			t.Fatal("故事危机来源不正确")
		}
		_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false-story", storyCase.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成故事误报"}, true)
		must(e)
		var story entity.StoryVersion
		must(root.Where("wish_id=? AND story_version=1", pub.ID).Take(&story).Error)
		if story.PublicBody != nil {
			t.Fatal("故事误报直接公开")
		}
		task = entity.ModerationTask{}
		must(root.Where("id=?", *story.ReviewTaskID).Take(&task).Error)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "review-story", task.ID, moderation.Decision{ExpectedVersion: task.Version, Decision: "approved"})
		must(e)
		must(root.Where("wish_id=? AND story_version=1", pub.ID).Take(&story).Error)
		if story.PublicBody == nil {
			t.Fatal("故事未重新走普通审")
		}
	})

	t.Run("尚存匹配中的留言和心愿重审不重配或上架", func(t *testing.T) {
		_, own := student()
		_, helper := student()
		pub := published(own)
		app, e := wishes.Apply(ctx, helper, uuid.NewString(), "apply-normal", pub.ID, wish.ApplicationInput{Message: "合成普通申请", WishContentVersion: pub.ContentVersion})
		must(e)
		scan("application", app.ID)
		var a entity.Application
		must(root.Where("id=?", app.ID).Take(&a).Error)
		var w entity.Wish
		must(root.Where("id=?", pub.ID).Take(&w).Error)
		_, e = wishes.AcceptApplication(ctx, own, uuid.NewString(), "accept-normal", a.ID, wish.AcceptInput{ExpectedVersion: a.Version, WishExpectedVersion: w.Version})
		must(e)
		for _, source := range []struct{ kind, id string }{{"application", app.ID}, {"wish", pub.ID}} {
			ev := event(source.kind, source.id)
			var task entity.ModerationTask
			must(root.Where("id=?", ev.AggregateID).Take(&task).Error)
			out, e := crisis.EscalateCrisis(ctx, operatorToken, uuid.NewString(), "patrol-matching", "task", task.ID, moderation.CrisisCommand{ExpectedVersion: task.Version, Reason: "合成匹配中内容风险"})
			must(e)
			_, e = crisis.ActOnCrisis(ctx, operatorToken, uuid.NewString(), "false-matching", out.ID, moderation.CrisisCommand{ExpectedVersion: 1, Reason: "合成匹配中误报"}, true)
			must(e)
			var next entity.ModerationTask
			must(root.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.kind=? AND c.entity_id=? AND t.crisis_recheck AND t.status='human_required'", source.kind, source.id).Take(&next).Error)
			_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "review-matching", next.ID, moderation.Decision{ExpectedVersion: next.Version, Decision: "approved"})
			must(e)
			must(root.Where("id=?", pub.ID).Take(&w).Error)
			must(root.Where("id=?", app.ID).Take(&a).Error)
			if w.State != "matching" || a.Status != "accepted" || count("public.matches", "wish_id=? AND ended_at IS NULL", pub.ID) != 1 {
				t.Fatal("误报复核改变了尚存匹配")
			}
		}
	})

}
