package moderation_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// privacyDependencies 只在隔离测试中模拟外部全版本删除与脱敏；持久日志仍往返独立PostgreSQL。
type privacyDependencies struct {
	fail, failAppend, lostAppend, failConfirm atomic.Bool
	journal                                   *privacy.PostgresJournal
	erasures                                  atomic.Int64
}

func (p *privacyDependencies) Append(ctx context.Context, in privacy.Intent) error {
	if p.failAppend.Load() {
		return errors.New("SYNTHETIC_JOURNAL_SECRET")
	}
	e := p.journal.Append(ctx, in)
	if e == nil && p.lostAppend.Load() {
		return errors.New("SYNTHETIC_LOST_ACK")
	}
	return e
}
func (p *privacyDependencies) Pending(ctx context.Context, after string, n int) ([]privacy.Intent, error) {
	return p.journal.Pending(ctx, after, n)
}
func (p *privacyDependencies) AllIntents(ctx context.Context, after string, n int) ([]privacy.Intent, error) {
	return p.journal.AllIntents(ctx, after, n)
}
func (p *privacyDependencies) Confirm(ctx context.Context, id string) error {
	if p.failConfirm.Load() {
		return errors.New("SYNTHETIC_CONFIRM_SECRET")
	}
	return p.journal.Confirm(ctx, id)
}
func (p *privacyDependencies) Erase(ctx context.Context, _ privacy.Intent, _ []privacy.Object) error {
	p.erasures.Add(1)
	if p.fail.Load() {
		return errors.New("SYNTHETIC_ERASE_SECRET")
	}
	return ctx.Err()
}
func (p *privacyDependencies) Redact(ctx context.Context, _ string, body string) (string, error) {
	if p.fail.Load() {
		return "", errors.New("SYNTHETIC_REDACTION_SECRET")
	}
	return strings.ReplaceAll(body, "合成个人姓名", "[已移除个人信息]"), ctx.Err()
}

// TestPrivacyDeletion 验证T031/REQ-004/005/007/008/014/017/018/019真实事务与六行矩阵。
// 中间件真实、身份及外部对象/脱敏合成，不作为正式全副本或90天恢复验收。
func TestPrivacyDeletion(t *testing.T) {
	dsn, recoveryDSN := os.Getenv("APP_MODERATION_TEST_DSN"), os.Getenv("APP_RECOVERY_TEST_DSN")
	if dsn == "" || recoveryDSN == "" {
		t.Skip("需独立业务及恢复PostgreSQL，运行make integration")
	}
	ctx := context.Background()
	log := zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	open := func(dsn string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		must(e)
		p, e := db.DB()
		must(e)
		t.Cleanup(func() { p.Close() })
		return db
	}
	fresh := func(dsn, prefix string) string {
		t.Helper()
		parent := open(dsn)
		var database string
		must(parent.Raw("SELECT current_database()").Scan(&database).Error)
		if (database != "liyuanci" && database != "postgres") || !strings.Contains(dsn, "dbname="+database+" ") {
			t.Fatal("需显式隔离测试父库DSN")
		}
		name := prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		must(parent.Exec("CREATE DATABASE " + name).Error)
		// 仅清理本用例创建的独立库；后注册的业务连接先关闭，不终止其他会话。
		t.Cleanup(func() {
			if e := parent.Exec("DROP DATABASE " + name).Error; e != nil {
				t.Error(e)
			}
		})
		return strings.Replace(dsn, "dbname="+database+" ", "dbname="+name+" ", 1)
	}
	dsn = fresh(dsn, "privacy_")
	recoveryDSN = fresh(recoveryDSN, "journal_")
	root, recoveryRoot := open(dsn), open(recoveryDSN)
	must(migration.Run(ctx, root, log, "up"))
	must(migration.ApplyPermissions(ctx, root, log))
	must(migration.RunJournal(ctx, recoveryRoot, log, "up"))
	must(migration.RunJournal(ctx, recoveryRoot, log, "permissions"))
	role := func(name string) *gorm.DB { return open(dsn + " options='-c role=" + name + "'") }
	codec, e := securedata.New(testKeys{}, log)
	must(e)
	j, e := privacy.NewJournal(ctx, open(recoveryDSN+" options='-c role=liyuanci_recovery_writer'"), open(recoveryDSN+" options='-c role=liyuanci_recovery_reader'"), codec, log, time.Second*5)
	must(e)
	deps := &privacyDependencies{journal: j}
	cc := command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 5}
	pc := privacy.Config{Command: cc, Key: bytes.Repeat([]byte{91}, 32), ReceiptTTL: time.Hour, RetryDelay: time.Millisecond, Redactor: deps}
	privacyDB, purgeAPI, purgeWorker := role("liyuanci_privacy_maintenance"), role("liyuanci_evidence_delete_api"), role("liyuanci_evidence_purger")
	service, e := privacy.New(privacyDB, deps, codec, deps, log, pc)
	must(e)
	evidence, e := privacy.New(purgeAPI, deps, codec, nil, log, pc)
	must(e)
	purger, e := privacy.New(purgeWorker, deps, codec, deps, log, pc)
	must(e)
	api, adminDB, workerDB := role("liyuanci_api"), role("liyuanci_admin_api"), role("liyuanci_domain_worker")
	wishes, e := wish.NewService(api, log, codec, nil, wish.ServiceConfig{Command: cc, ReceiptKey: bytes.Repeat([]byte{92}, 32), MaxStoryBytes: 65536})
	must(e)
	reviews, e := moderation.New(adminDB, log, codec, moderation.Config{Command: cc, CursorKey: bytes.Repeat([]byte{93}, 32), MaxEntries: 100})
	must(e)
	worker, e := moderation.NewWorker(workerDB, log, codec, &analyzer{}, moderation.WorkerConfig{AnalysisTimeout: time.Second, MessageAnalyzer: privateAnalyzer{}, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}})
	must(e)
	chats, e := chat.New(api, log, codec, chat.Config{Command: cc, Key: bytes.Repeat([]byte{94}, 32), MaxBodyBytes: 65536})
	must(e)
	admin, adminToken := uuid.NewString(), uuid.NewString()
	credential := "synthetic-privacy-reviewer"
	must(root.Create(&entity.AdminAccount{ID: admin, CredentialRef: &credential}).Error)
	must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "review_operator"}).Error)
	session := func(id, token string, admin bool) {
		t.Helper()
		h := sha256.Sum256([]byte(token))
		s := entity.Session{ID: uuid.NewString(), TokenHash: h[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}
		if admin {
			s.AdminID = &id
		} else {
			s.AccountID = &id
		}
		must(root.Create(&s).Error)
	}
	session(admin, adminToken, true)
	for _, r := range []struct{ kind, word string }{{"crisis", "危机命中"}, {"school", "学校命中"}, {"ordinary_violation", "普通命中"}} {
		var h entity.WordlistHead
		must(root.Where("kind=?", r.kind).Take(&h).Error)
		d, e := reviews.CreateWordlist(ctx, adminToken, uuid.NewString(), "rule", moderation.WordlistInput{Kind: r.kind, Entries: []string{r.word}, ExpectedVersion: h.Version})
		must(e)
		must(root.Where("kind=?", r.kind).Take(&h).Error)
		_, e = reviews.PublishWordlist(ctx, adminToken, uuid.NewString(), "rule", d.ID, moderation.WordlistPublish{ExpectedVersion: d.Version, ExpectedHeadVersion: h.Version})
		must(e)
	}
	student := func() (string, string) {
		t.Helper()
		id, token := uuid.NewString(), uuid.NewString()
		must(root.Create(&entity.Account{ID: id, Nickname: "合成参与人", Verification: "verified"}).Error)
		session(id, token, false)
		return id, token
	}
	wr := func(id string) entity.Wish {
		t.Helper()
		var w entity.Wish
		must(root.Where("id=?", id).Take(&w).Error)
		return w
	}
	event := func(kind, id string) durable.Event {
		t.Helper()
		var e durable.Event
		must(root.Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.kind=? AND c.entity_id=? AND e.superseded_at IS NULL ORDER BY t.created_at DESC LIMIT 1`, kind, id).Scan(&e).Error)
		if e.EventID == "" {
			t.Fatal("缺事件")
		}
		return e
	}
	scan := func(kind, id string) {
		t.Helper()
		out, e := worker.Consume(ctx, event(kind, id))
		must(e)
		if !out.Applied {
			t.Fatalf("扫描未应用: %+v", out)
		}
	}
	published := func(token string) wish.CreatedDraft {
		t.Helper()
		w, e := wishes.Create(ctx, token, uuid.NewString(), "create", wish.ContentInput{Category: "help", Campus: "wushan", Body: "自己的心愿合成个人姓名"})
		must(e)
		w, e = wishes.Submit(ctx, token, uuid.NewString(), "submit", w.ID, w.Version, w.ContentVersion)
		must(e)
		scan("wish", w.ID)
		w, e = wishes.Publish(ctx, token, uuid.NewString(), "publish", w.ID, wr(w.ID).Version)
		must(e)
		return w
	}
	count := func(db *gorm.DB, table, query string, args ...any) int64 {
		t.Helper()
		var n int64
		must(db.Table(table).Where(query, args...).Count(&n).Error)
		return n
	}
	force := func(id string) {
		must(root.Model(&entity.DeletionJob{}).Where("job_id=?", id).Update("next_at", time.Now().Add(-time.Second)).Error)
	}
	drain := func(scope string, s *privacy.Service) {
		t.Helper()
		for i := 0; i < 20; i++ {
			ok, e := s.ProcessNext(ctx, scope)
			must(e)
			if !ok {
				return
			}
		}
		t.Fatal("清理未收敛")
	}
	request := func(token string) privacy.Accepted {
		t.Helper()
		out, e := service.Request(ctx, token, uuid.NewString(), "delete", privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: 1})
		must(e)
		if out.Status != "processing" || !strings.Contains(out.Receipt, ".") {
			t.Fatal("不是匿名处理中回执")
		}
		return out
	}
	t.Run("资料反馈注销即退出统计且恢复备份重删", func(t *testing.T) {
		owner, token := student()
		peer, peerToken := student()
		cfg := resourceplaza.Config{Command: cc, Key: bytes.Repeat([]byte{97}, 32)}
		resources, e := resourceplaza.New(api, log, codec, cfg)
		must(e)
		managed, e := resourceplaza.New(adminDB, log, codec, cfg)
		must(e)
		write := func(s *resourceplaza.Service, tok, key, op, id string, in resourceplaza.Input) map[string]any {
			t.Helper()
			out, e := s.Write(ctx, tok, "privacy-resource", key, op, id, in)
			must(e)
			return out.(map[string]any)
		}
		cat := write(managed, adminToken, uuid.NewString(), "category_create", "", resourceplaza.Input{Name: "注销测试分类", Enabled: true})
		res := write(managed, adminToken, uuid.NewString(), "create", "", resourceplaza.Input{Title: "共享合成资料", CategoryID: cat["id"].(string), DownloadURL: "https://example.test/resource"})
		id := res["id"].(string)
		write(managed, adminToken, uuid.NewString(), "publish", id, resourceplaza.Input{ExpectedVersion: 1})
		key := uuid.NewString()
		vote := resourceplaza.Input{Kind: "valid", ExpectedContentVersion: 1}
		write(resources, token, key, "feedback", id, vote)
		write(resources, peerToken, uuid.NewString(), "feedback", id, resourceplaza.Input{Kind: "incomplete", ExpectedContentVersion: 1})
		assertTotal := func(want int64) {
			t.Helper()
			out, e := managed.Read(ctx, adminToken, "privacy-resource-summary", "detail", id, resourceplaza.Query{}, true)
			must(e)
			data := out.(map[string]any)
			if data["status"] != "published" || data["feedback_summary"].(map[string]any)["total"] != want {
				t.Fatalf("共享资料状态或当前有效反馈总数错误: %+v", data["feedback_summary"])
			}
		}
		assertTotal(2)
		// 保留旧备份中的关联，随后恢复它们验证已确认删除意图仍会重删。
		var oldFeedback entity.ResourceFeedback
		var oldReceipt entity.CommandReceipt
		must(root.Where("resource_id=? AND account_id=?", id, owner).Take(&oldFeedback).Error)
		must(root.Where("actor_kind='student' AND actor_id=? AND result_ref=?", owner, oldFeedback.ID).Take(&oldReceipt).Error)
		request(token)
		assertTotal(1)
		if count(root, "public.resource_feedback", "account_id=?", owner) != 1 {
			t.Fatal("未验证异步清理前的注销统计屏障")
		}
		if _, e = resources.Read(ctx, token, "deleted-read", "detail", id, resourceplaza.Query{}, false); !errors.Is(e, command.ErrUnauthenticated) {
			t.Fatal("注销会话仍可读资料", e)
		}
		if _, e = resources.Write(ctx, token, "deleted-replay", key, "feedback", id, vote); !errors.Is(e, command.ErrUnauthenticated) {
			t.Fatal("注销后旧成功反馈回执仍可重放", e)
		}
		assertClean := func() {
			t.Helper()
			if count(root, "public.resource_feedback", "account_id=?", owner) != 0 || count(root, "public.command_receipts", "actor_kind='student' AND actor_id=?", owner) != 0 || count(root, "public.accounts", "id=?", owner) != 0 {
				t.Fatal("注销反馈、主体或回执残留")
			}
			if count(root, "public.resource_materials", "id=?", id) != 1 || count(root, "public.resource_categories", "id=?", cat["id"]) != 1 || count(root, "public.resource_feedback", "account_id=? AND resource_id=?", peer, id) != 1 {
				t.Fatal("注销误删后台共享资料、分类或其他账号反馈")
			}
			assertTotal(1)
		}
		drain("account", service)
		assertClean()
		must(root.Create(&entity.Account{ID: owner, Nickname: "合成恢复账号", Verification: "verified"}).Error)
		must(root.Create(&oldFeedback).Error)
		must(root.Create(&oldReceipt).Error)
		_, e = service.Recover(ctx, "", 100, "account")
		must(e)
		assertTotal(1)
		drain("account", service)
		assertClean()
	})
	t.Run("日志失败和版本错误不推进注销", func(t *testing.T) {
		id, token := student()
		deps.failAppend.Store(true)
		_, e := service.Request(ctx, token, "fail", "delete", privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: 1})
		if e == nil {
			t.Fatal("日志不可用仍成功")
		}
		deps.failAppend.Store(false)
		if count(root, "public.accounts", "id=? AND lifecycle='active'", id) != 1 {
			t.Fatal("日志失败推进注销")
		}
		before := count(recoveryRoot, "public.recovery_delete_journal", "true")
		_, e = service.Request(ctx, token, "stale", "delete", privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: 2})
		if !errors.Is(e, command.ErrVersionConflict) {
			t.Fatal("旧版本未拒绝", e)
		}
		if count(recoveryRoot, "public.recovery_delete_journal", "true") != before {
			t.Fatal("旧版本产生意图")
		}
	})
	t.Run("独立意图确认丢失后重建屏障并重试", func(t *testing.T) {
		id, token := student()
		deps.lostAppend.Store(true)
		_, e := service.Request(ctx, token, "lost", "delete", privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: 1})
		if e == nil {
			t.Fatal("丢失日志确认未失败")
		}
		deps.lostAppend.Store(false)
		if count(root, "public.accounts", "id=? AND lifecycle='active'", id) != 1 {
			t.Fatal("测试未模拟本库回滚")
		}
		_, e = service.Reconcile(ctx, "", 100, "account")
		must(e)
		if count(root, "public.accounts", "id=? AND lifecycle='deleting'", id) != 1 {
			t.Fatal("独立意图未恢复注销")
		}
		drain("account", service)
		if count(root, "public.accounts", "id=?", id) != 0 {
			t.Fatal("未物理删除")
		}
	})
	t.Run("外部删除失败及独立确认失败保留处理中", func(t *testing.T) {
		id, token := student()
		out := request(token)
		job, _, _ := strings.Cut(out.Receipt, ".")
		deps.fail.Store(true)
		_, e := service.ProcessNext(ctx, "account")
		if e == nil {
			t.Fatal("外部失败仍完成")
		}
		deps.fail.Store(false)
		status, e := service.Status(ctx, "receipt", out.Receipt)
		must(e)
		if status.Status != "processing" || count(root, "public.accounts", "id=?", id) != 1 {
			t.Fatal("失败提前删除")
		}
		force(job)
		deps.failConfirm.Store(true)
		_, e = service.ProcessNext(ctx, "account")
		if e == nil {
			t.Fatal("确认失败仍成功")
		}
		status, e = service.Status(ctx, "receipt", out.Receipt)
		must(e)
		if status.Status != "processing" {
			t.Fatal("确认失败提前完成")
		}
		deps.failConfirm.Store(false)
		force(job)
		drain("account", service)
		status, e = service.Status(ctx, "receipt", out.Receipt)
		must(e)
		if status.Status != "completed" {
			t.Fatal("确认重试不收敛")
		}
		if _, e = service.Status(ctx, "receipt", job); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("裸UUID可查")
		}
		must(root.Model(&entity.DeletionJob{}).Where("job_id=?", job).Update("expires_at", time.Now().Add(-time.Second)).Error)
		if _, e = service.Status(ctx, "receipt", out.Receipt); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("过期仍可查")
		}
	})
	for _, deleted := range []string{"requester", "helper"} {
		for _, state := range []string{"matching", "realized", "fulfilled"} {
			t.Run(deleted+"_"+state, func(t *testing.T) {
				owner, ot := student()
				helper, ht := student()
				w := published(ot)
				app, e := wishes.Apply(ctx, ht, uuid.NewString(), "apply", w.ID, wish.ApplicationInput{Message: "合成留言", WishContentVersion: w.ContentVersion})
				must(e)
				scan("application", app.ID)
				var a entity.Application
				must(root.Where("id=?", app.ID).Take(&a).Error)
				match, e := wishes.AcceptApplication(ctx, ot, uuid.NewString(), "accept", app.ID, wish.AcceptInput{ExpectedVersion: a.Version, WishExpectedVersion: wr(w.ID).Version})
				must(e)
				sent, e := chats.Send(ctx, ot, uuid.NewString(), "send", match.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "双方普通消息", ExpectedWriteEpoch: 1})
				must(e)
				scan("message", sent.MessageID)
				pending, e := chats.Send(ctx, ht, uuid.NewString(), "pending", match.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "待审消息", ExpectedWriteEpoch: 1})
				must(e)
				late := event("message", pending.MessageID)
				if state != "matching" {
					_, e = wishes.Complete(ctx, ot, uuid.NewString(), "complete", w.ID, wr(w.ID).Version)
					must(e)
				}
				var oldStory durable.Event
				if state == "fulfilled" {
					_, e = wishes.SubmitStory(ctx, ot, uuid.NewString(), "story", w.ID, wish.StoryInput{Body: "自己的故事合成个人姓名", WishExpectedVersion: wr(w.ID).Version})
					must(e)
					scan("story", w.ID)
					oldStory = event("story", w.ID)
				}
				before := wr(w.ID)
				target, token := owner, ot
				survivor := ht
				if deleted == "helper" {
					target, token = helper, ht
					survivor = ot
				}
				out := request(token)
				if count(root, "public.sessions", "account_id=? AND revoked_at IS NULL", target) != 0 {
					t.Fatal("会话未撤销")
				}
				if _, e = chats.Messages(ctx, survivor, "hidden", match.ConversationID, chat.Query{Limit: 50}); e == nil {
					t.Fatal("注销生效仍读聊天")
				}
				_, e = worker.Consume(ctx, late)
				must(e)
				if deleted == "helper" {
					current := wr(w.ID)
					want := "withdrawn"
					if state == "fulfilled" {
						want = "fulfilled"
					}
					if current.State != want || current.ExpiresAt == nil || !current.ExpiresAt.Equal(*before.ExpiresAt) || !current.PeerDeleted {
						t.Fatal("六行状态/期限错误")
					}
					own, e := wishes.Detail(ctx, ot, "own", w.ID)
					must(e)
					if !own.PrivacyReviewRequired || own.OwnContent != nil {
						t.Fatal("作者能读屏障旧稿")
					}
					if state == "fulfilled" {
						if _, e = wishes.Story(ctx, "", "public", w.ID); !errors.Is(e, command.ErrNotFound) {
							t.Fatal("屏障公开旧故事")
						}
					}
				}
				drain("account", service)
				if count(root, "public.accounts", "id=?", target) != 0 || count(root, "public.messages", "conversation_id=?", match.ConversationID) != 0 || count(root, "public.matches", "id=?", match.MatchID) != 0 {
					t.Fatal("普通关联未清理")
				}
				if count(root, "public.command_receipts", "actor_kind='student' AND actor_id=?", target) != 0 {
					t.Fatal("主体回执残留")
				}
				status, e := service.Status(ctx, "receipt", out.Receipt)
				must(e)
				if status.Status != "completed" {
					t.Fatal("未完成")
				}
				replay, e := worker.Consume(ctx, late)
				must(e)
				if replay.Applied {
					t.Fatal("已删除晚到重建")
				}
				if deleted == "requester" {
					if count(root, "public.wishes", "id=?", w.ID) != 0 {
						t.Fatal("许愿人内容未删")
					}
					return
				}
				newWish := wr(w.ID)
				if count(root, "public.privacy_holds", "target_id=? AND status='active'", w.ID) == 0 {
					t.Fatal("删除关系解除屏障")
				}
				if state != "fulfilled" && newWish.State != "withdrawn" {
					t.Fatal("注销清理自动送审")
				}
				if count(root, "public.work_items", "target_id IN (SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry c ON c.id=t.content_id WHERE t.privacy_recheck AND c.entity_id=?)", w.ID) != 0 {
					t.Fatal("清理未经本人操作自动重审")
				}
				// 作者显式提交脱敏新心愿；故事改稿也由本人独立发起。
				_, e = wishes.Submit(ctx, ot, uuid.NewString(), "privacy-submit", w.ID, newWish.Version, newWish.CurrentContentVersion)
				must(e)
				if state == "fulfilled" {
					_, e = worker.Consume(ctx, oldStory)
					must(e)
					_, e = wishes.UpdateStory(ctx, ot, uuid.NewString(), "privacy-story", w.ID, wish.StoryUpdate{Body: "本人显式重提的脱敏故事", ExpectedVersion: wr(w.ID).Version})
					must(e)
					deferred, e := worker.Consume(ctx, event("story", w.ID))
					must(e)
					if !deferred.Deferred {
						t.Fatal("父屏障未解除时故事未保留待审")
					}
				}
				scan("wish", w.ID)
				if state == "fulfilled" {
					_, otherToken := student()
					other, e := wishes.Create(ctx, otherToken, uuid.NewString(), "other", wish.ContentInput{Category: "help", Campus: "wushan", Body: "不相关内容不受父审批阻塞"})
					must(e)
					_, e = wishes.Submit(ctx, otherToken, uuid.NewString(), "other", other.ID, other.Version, other.ContentVersion)
					must(e)
					ran, e := worker.ProcessNext(ctx)
					must(e)
					if !ran.Applied {
						t.Fatal("等待父审批的故事阻塞普通扫描")
					}
				}
				var v entity.WishVersion
				must(root.Where("wish_id=? AND content_version=?", w.ID, newWish.CurrentContentVersion).Take(&v).Error)
				var task entity.ModerationTask
				must(root.Where("id=?", *v.ReviewTaskID).Take(&task).Error)
				if task.Status != "human_required" {
					t.Fatal("脱敏新稿自动放行")
				}
				_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "review", task.ID, moderation.Decision{Decision: "approved", ExpectedVersion: task.Version})
				must(e)
				if state == "fulfilled" {
					if wr(w.ID).State != "fulfilled" {
						t.Fatal("隐私重审改变已还愿")
					}
					scan("story", w.ID)
					ev := event("story", w.ID)
					task = entity.ModerationTask{}
					must(root.Where("id=?", ev.AggregateID).Take(&task).Error)
					_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "review", task.ID, moderation.Decision{Decision: "approved", ExpectedVersion: task.Version})
					must(e)
					story, e := wishes.Story(ctx, "", "public", w.ID)
					must(e)
					if strings.Contains(story.Body, "合成个人姓名") {
						t.Fatal("未脱敏")
					}
				}
				if state != "fulfilled" && wr(w.ID).State != "ready" {
					t.Fatal("应仅到待上架")
				}
			})
		}
	}

	t.Run("历史应愿人注销不终结新的合法匹配", func(t *testing.T) {
		owner, ot := student()
		helper, ht := student()
		oldHelper, oldToken := student()
		w := published(ot)
		app, e := wishes.Apply(ctx, ht, uuid.NewString(), "apply", w.ID, wish.ApplicationInput{Message: "新的合法关系", WishContentVersion: w.ContentVersion})
		must(e)
		scan("application", app.ID)
		var a entity.Application
		must(root.Where("id=?", app.ID).Take(&a).Error)
		current, e := wishes.AcceptApplication(ctx, ot, uuid.NewString(), "accept", app.ID, wish.AcceptInput{ExpectedVersion: a.Version, WishExpectedVersion: wr(w.ID).Version})
		must(e)
		ended := time.Now()
		oldMatch := uuid.NewString()
		must(root.Create(&entity.Match{ID: oldMatch, WishID: w.ID, RequesterID: owner, HelperID: oldHelper, Status: "ended", EndedAt: &ended}).Error)
		expiry := wr(w.ID).ExpiresAt
		request(oldToken)
		drain("account", service)
		row := wr(w.ID)
		if row.State != "matching" || !row.ExpiresAt.Equal(*expiry) || count(root, "public.matches", "id=? AND status='current' AND helper_id=?", current.MatchID, helper) != 1 {
			t.Fatal("历史注销改变当前新关系")
		}
		_, e = wishes.Submit(ctx, ot, uuid.NewString(), "resubmit", w.ID, row.Version, row.CurrentContentVersion)
		must(e)
		scan("wish", w.ID)
		ev := event("wish", w.ID)
		var task entity.ModerationTask
		must(root.Where("id=?", ev.AggregateID).Take(&task).Error)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "approve", task.ID, moderation.Decision{Decision: "approved", ExpectedVersion: task.Version})
		must(e)
		if wr(w.ID).State != "matching" || count(root, "public.matches", "id=? AND status='current'", current.MatchID) != 1 {
			t.Fatal("隐私复审重建或解除匹配")
		}
		// 清理夹具仍走正式账号删除，不向现有日常资源写入。
		request(ot)
		drain("account", service)
	})
	t.Run("超管删除证据保持普通隔离并支持独立恢复重删", func(t *testing.T) {
		owner, token := student()
		must(root.Create(&entity.IdentityLookupKey{Purpose: "student_number", CurrentRef: "synthetic-lookup", Version: 1}).Error)
		env, e := codec.Encrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: owner, Version: 1}, []byte("SYNTHETIC_NUMBER"))
		must(e)
		raw, e := json.Marshal(env)
		must(e)
		must(root.Create(&entity.IdentitySecret{AccountID: owner, Purpose: "student_number", ContentVersion: 1, Envelope: raw}).Error)
		w, e := wishes.Create(ctx, token, uuid.NewString(), "crisis", wish.ContentInput{Category: "help", Campus: "wushan", Body: "危机命中"})
		must(e)
		w, e = wishes.Submit(ctx, token, uuid.NewString(), "submit", w.ID, w.Version, w.ContentVersion)
		must(e)
		scan("wish", w.ID)
		var ec entity.EvidenceCase
		must(root.Order("created_at DESC").Take(&ec).Error)
		// 先删除普通账号：限定证据继续独立存在且学号可授权读取。
		request(token)
		drain("account", service)
		crisis, e := moderation.New(role("liyuanci_crisis_api"), log, codec, moderation.Config{Command: cc, CursorKey: bytes.Repeat([]byte{96}, 32), MaxEntries: 100})
		must(e)
		must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "crisis_operator"}).Error)
		retained, e := crisis.CrisisEvidence(ctx, adminToken, "retained", ec.ID)
		must(e)
		if retained.StudentNumber != "SYNTHETIC_NUMBER" {
			t.Fatal("普通删除破坏限定证据")
		}
		if _, e = evidence.DeleteEvidence(ctx, adminToken, "forbid", "delete", ec.ID, privacy.EvidenceInput{ExpectedVersion: 1, Reason: "合成测试删除"}); !errors.Is(e, command.ErrForbidden) {
			t.Fatal("普通运营可删", e)
		}
		must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "super_admin"}).Error)
		out, e := evidence.DeleteEvidence(ctx, adminToken, "evidence", "delete", ec.ID, privacy.EvidenceInput{ExpectedVersion: 1, Reason: "合成测试删除"})
		must(e)
		if out.Status != "pending" {
			t.Fatal("未持久受理")
		}
		if count(root, "evidence.evidence_cases", "id=? AND status='deletion_pending'", ec.ID) != 1 {
			t.Fatal("未立即阻断证据")
		}
		if count(root, "public.wishes", "id=?", w.ID) != 0 {
			t.Fatal("删除证据恢复普通账号内容")
		}
		drain("evidence", purger)
		if count(root, "evidence.evidence_cases", "id=?", ec.ID) != 0 {
			t.Fatal("未删证据")
		}
		// 模拟业务旧备份恢复了已删除证据，独立日志保留已确认意图，重新施加删除。
		must(root.Create(&entity.EvidenceCase{ID: ec.ID, Kind: ec.Kind, SourceRef: ec.SourceRef}).Error)
		must(root.Where("job_id=?", out.ID).Delete(&entity.DeletionJob{}).Error)
		_, e = purger.Recover(ctx, "", 100, "evidence")
		must(e)
		drain("evidence", purger)
		if count(root, "evidence.evidence_cases", "id=?", ec.ID) != 0 {
			t.Fatal("已确认日志未重删恢复副本")
		}
		// 账号注销不应保留无关普通身份；危机限制无证据也不会让正文回流。
	})

	t.Run("有效来源删证据不解除普通隔离及角色撤销拒绝重放", func(t *testing.T) {
		owner, token := student()
		env, e := codec.Encrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: owner, Version: 1}, []byte("SYNTHETIC_SECOND_NUMBER"))
		must(e)
		raw, e := json.Marshal(env)
		must(e)
		must(root.Create(&entity.IdentitySecret{AccountID: owner, Purpose: "student_number", ContentVersion: 1, Envelope: raw}).Error)
		w, e := wishes.Create(ctx, token, uuid.NewString(), "crisis", wish.ContentInput{Category: "help", Campus: "wushan", Body: "危机命中"})
		must(e)
		w, e = wishes.Submit(ctx, token, uuid.NewString(), "submit", w.ID, w.Version, w.ContentVersion)
		must(e)
		scan("wish", w.ID)
		var ec entity.EvidenceCase
		must(root.Where("source_ref IN (SELECT id FROM public.content_registry WHERE entity_id=?)", w.ID).Take(&ec).Error)
		in := privacy.EvidenceInput{ExpectedVersion: ec.Version, Reason: "合成最小化清理"}
		out, e := evidence.DeleteEvidence(ctx, adminToken, "role-replay", "delete", ec.ID, in)
		must(e)
		replay, e := evidence.DeleteEvidence(ctx, adminToken, "role-replay", "delete", ec.ID, in)
		must(e)
		if replay.ID != out.ID {
			t.Fatal("同键重复建工作")
		}
		must(root.Where("admin_id=? AND role='super_admin'", admin).Delete(&entity.AdminRoleBinding{}).Error)
		if _, e = evidence.DeleteEvidence(ctx, adminToken, "role-replay", "delete", ec.ID, in); !errors.Is(e, command.ErrForbidden) {
			t.Fatal("撤销角色仍能重放")
		}
		must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "super_admin"}).Error)
		drain("evidence", purger)
		if count(root, "public.content_restrictions", "content_id=? AND kind='crisis' AND active", ec.SourceRef) != 1 {
			t.Fatal("删除证据解除普通隔离")
		}
		status, e := evidence.EvidenceStatus(ctx, adminToken, "progress", out.ID)
		must(e)
		if status.Status != "completed" {
			t.Fatal("证据进度未完成")
		}
		request(token)
		drain("account", service)
	})
	t.Run("受罚本人及普通资产审计通知完整清理", func(t *testing.T) {
		id, token := student()
		peer, _ := student()
		asset := uuid.NewString()
		env, e := codec.Encrypt(ctx, securedata.Binding{Purpose: "student_card_key", ObjectID: asset, Version: 1}, []byte("synthetic-private-object"))
		must(e)
		raw, e := json.Marshal(env)
		must(e)
		must(root.Create(&entity.Asset{ID: asset, OwnerID: id, Purpose: "student_card", EncryptedObjectKey: raw, ObjectVersion: "synthetic-v1", WrappedKeyRef: "synthetic-wrap"}).Error)
		review := uuid.NewString()
		must(root.Create(&entity.IdentityReview{ID: review, AccountID: id, AssetID: asset}).Error)
		penalty := uuid.NewString()
		must(root.Create(&entity.Penalty{ID: penalty, AccountID: id, Level: "severe", ActionKind: "ban", ViolationRef: uuid.NewString(), ContentVersion: 1, ReasonRef: uuid.NewString(), StartsAt: time.Now()}).Error)
		must(root.Create(&entity.AccessAudit{ID: uuid.NewString(), ActorID: admin, SubjectRef: review, Purpose: "synthetic.read", Outcome: "allowed"}).Error)
		// 后台普通资料浏览必须由真实查询写入目标关联，注销后由原清理链删除。
		users, e := safety.New(adminDB, log, codec, safety.Config{Command: cc, Key: bytes.Repeat([]byte{95}, 32), MaxTextBytes: 65536})
		must(e)
		_, e = users.UserDetailRead(ctx, adminToken, "users-read-before-delete", id)
		must(e)
		_, e = users.UserPenalties(ctx, adminToken, "users-history-before-delete", id, safety.UsersQuery{Limit: 20})
		must(e)
		if count(root, "public.access_audits", "subject_ref=? AND purpose IN ('admin_user_detail','admin_user_penalties')", id) != 2 {
			t.Fatal("用户查询未写入可清理审计")
		}
		notification := uuid.NewString()
		must(root.Create(&entity.Notification{ID: notification, RecipientID: peer, Type: "synthetic_notice", TargetRef: asset}).Error)
		must(root.Create(&entity.DeliveryAttempt{ID: uuid.NewString(), NotificationID: notification, Channel: "in_app", Status: "sent"}).Error)
		out := request(token)
		drain("account", service)
		for _, spec := range []struct {
			table, query string
			arg          string
		}{{"public.assets", "owner_id=?", id}, {"public.identity_reviews", "account_id=?", id}, {"public.penalties", "account_id=?", id}, {"public.access_audits", "subject_ref=?", review}, {"public.access_audits", "subject_ref=?", id}, {"public.notifications", "id=?", notification}, {"public.delivery_attempts", "notification_id=?", notification}} {
			if count(root, spec.table, spec.query, spec.arg) != 0 {
				t.Fatal("普通资料残留", spec.table)
			}
		}
		status, e := service.Status(ctx, "receipt", out.Receipt)
		must(e)
		if status.Status != "completed" {
			t.Fatal("受罚注销未完成")
		}
	})
	t.Run("缺少清理适配器不声称完成", func(t *testing.T) {
		id, token := student()
		blocked, e := privacy.New(privacyDB, deps, codec, nil, log, pc)
		must(e)
		out := request(token)
		job, _, _ := strings.Cut(out.Receipt, ".")
		if _, e = blocked.ProcessNext(ctx, "account"); e == nil {
			t.Fatal("缺少适配器成功")
		}
		status, e := service.Status(ctx, "receipt", out.Receipt)
		must(e)
		if status.Status != "processing" || count(root, "public.accounts", "id=?", id) != 1 {
			t.Fatal("缺适配器提前完成")
		}
		force(job)
		drain("account", service)
	})
	t.Run("并发注销只持久一份有效意图", func(t *testing.T) {
		id, token := student()
		type result struct {
			a privacy.Accepted
			e error
		}
		results := make(chan result, 2)
		for i := 0; i < 2; i++ {
			go func() {
				a, e := service.Request(ctx, token, uuid.NewString(), "concurrent", privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: 1})
				results <- result{a, e}
			}()
		}
		successes := 0
		for i := 0; i < 2; i++ {
			r := <-results
			if r.e == nil {
				successes++
			}
		}
		if successes != 1 || count(root, "public.deletion_jobs", "subject_ref=?", id) != 1 {
			t.Fatal("并发注销重复生效")
		}
		drain("account", service)
		// 即使恢复快照错误地留着completed工作，也不能跳过恢复的普通主体。
		must(root.Create(&entity.Account{ID: id, Nickname: "合成恢复账号", Verification: "verified"}).Error)
		_, e := service.Recover(ctx, "", 100, "account")
		must(e)
		drain("account", service)
		if count(root, "public.accounts", "id=?", id) != 0 {
			t.Fatal("恢复的普通主体未重删")
		}
	})
	t.Run("HTTP严格字段匿名回执与禁止缓存", func(t *testing.T) {
		_, token := student()
		r := httpapi.New(log, func(context.Context) error { return nil })
		must(httpapi.RegisterPrivacy(r, service))
		must(httpapi.RegisterEvidenceDeletion(r, evidence))
		for _, tc := range []struct {
			body   string
			status int
		}{{`{"confirmation":"delete_account","expected_version":1,"account_id":"other"}`, 400}, {`{"confirmation":"yes","expected_version":1}`, 400}, {`{"confirmation":"delete_account","expected_version":0}`, 400}, {`{"confirmation":"delete_account","expected_version":1}`, 202}} {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/me/deletion-requests", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Idempotency-Key", uuid.NewString())
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.status || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("HTTP响应错误", rr.Code, rr.Body.String())
			}
		}
		drain("account", service)
	})
	t.Run("独立意图超过百条仍可分页覆盖", func(t *testing.T) {
		created := map[string]bool{}
		for i := 0; i < 105; i++ {
			in := privacy.Intent{ID: uuid.NewString(), Target: uuid.NewString(), Scope: "evidence"}
			must(j.Append(ctx, in))
			created[in.ID] = true
		}
		after := ""
		seen := map[string]bool{}
		for i := 0; i < 20; i++ {
			rows, e := j.Pending(ctx, after, 17)
			must(e)
			for _, r := range rows {
				if seen[r.ID] {
					t.Fatal("分页重复")
				}
				seen[r.ID] = true
				after = r.ID
			}
			if len(rows) < 17 {
				break
			}
		}
		for id := range created {
			if !seen[id] {
				t.Fatal("旧待办阻塞超过百条的新意图")
			}
			must(j.Confirm(ctx, id))
		}
	})

	if role("liyuanci_api").Exec("DELETE FROM public.accounts WHERE false").Error == nil {
		t.Fatal("普通API有物理删除权限")
	}
	if privacyDB.Exec("SELECT * FROM evidence.evidence_items LIMIT 0").Error == nil {
		t.Fatal("普通清理可读证据")
	}
	if purgeAPI.Exec("DELETE FROM evidence.evidence_cases WHERE false").Error == nil {
		t.Fatal("证据受理池可物理删除")
	}
}
