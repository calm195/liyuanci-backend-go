package moderation_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// TestChatDelivery 覆盖T021、REQ-006/007/015/018和AR-001的真实事务、HTTP与MQ。
// 身份、密钥、词库及文本分析均合成；已实现起点由夹具准备，不宣称完成T023。
func TestChatDelivery(t *testing.T) {
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
	database := "chat_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	wishes, e := wish.NewService(api, logger, codec, nil, wish.ServiceConfig{Command: cfg, ReceiptKey: []byte(strings.Repeat("a", 32))})
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
	appRow := func(id string) entity.Application {
		t.Helper()
		var row entity.Application
		must(root.Where("id=?", id).Take(&row).Error)
		return row
	}
	wishRow := func(id string) entity.Wish {
		t.Helper()
		var row entity.Wish
		must(root.Where("id=?", id).Take(&row).Error)
		return row
	}
	taskRow := func(id string) entity.ModerationTask {
		t.Helper()
		var row entity.ModerationTask
		must(root.Where("id=?", id).Take(&row).Error)
		return row
	}
	apply := func(token string, w wish.CreatedDraft, message string) wish.ApplicationResult {
		t.Helper()
		out, e := wishes.Apply(ctx, token, uuid.NewString(), "apply", w.ID, wish.ApplicationInput{Message: message, WishContentVersion: w.ContentVersion})
		must(e)
		return out
	}
	accept := func(owner string, w wish.CreatedDraft, app string) wish.MatchResult {
		t.Helper()
		a := appRow(app)
		out, e := wishes.AcceptApplication(ctx, owner, uuid.NewString(), "accept", app, wish.AcceptInput{ExpectedVersion: a.Version, WishExpectedVersion: wishRow(w.ID).Version})
		must(e)
		return out
	}
	chats, e := chat.New(api, logger, codec, chat.Config{Command: cfg, Key: []byte(strings.Repeat("c", 32)), MaxBodyBytes: 64 << 10})
	must(e)
	type fixture struct {
		ownerID, helperID, owner, helper string
		w                                wish.CreatedDraft
		m                                wish.MatchResult
	}
	matched := func() fixture {
		t.Helper()
		oi, ot := student()
		hi, ht := student()
		w := published(ot)
		a := apply(ht, w, "应愿合成留言")
		scan("application", a.ID)
		m := accept(ot, w, a.ID)
		return fixture{oi, hi, ot, ht, w, m}
	}
	send := func(f fixture, body string) chat.Accepted {
		t.Helper()
		out, e := chats.Send(ctx, f.owner, uuid.NewString(), "send", f.m.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: body, ExpectedWriteEpoch: 1})
		must(e)
		if out.Status != "pending_scan" || out.DeliverySeq != nil {
			t.Fatal("提交提前送达")
		}
		return out
	}
	message := func(id string) entity.Message {
		t.Helper()
		var m entity.Message
		must(root.Where("id=?", id).Take(&m).Error)
		return m
	}
	task := func(id string) entity.ModerationTask { return taskRow(*message(id).ReviewTaskID) }
	page := func(f fixture, after int64) chat.MessagesPage {
		t.Helper()
		out, e := chats.Messages(ctx, f.helper, "read", f.m.ConversationID, chat.Query{AfterSeq: &after, Limit: 50})
		must(e)
		return out
	}
	t.Run("AR001慢审后达与私聊联系方式", func(t *testing.T) {
		f := matched()
		body := "普通命中 联系方式 wx:synthetic_only 学校命中"
		a := send(f, body)
		b := send(f, "先送达")
		scan("message", a.MessageID)
		if got := page(f, 0); len(got.Items) != 0 || got.NextAfterSeq != 0 {
			t.Fatal("待审消息泄漏或空页推进")
		}
		scan("message", b.MessageID)
		first := page(f, 0)
		if len(first.Items) != 1 || first.Items[0].MessageID != b.MessageID || first.NextAfterSeq != 1 || first.Items[0].ViewerIsSender {
			t.Fatal("后提交应先获得序号1")
		}
		ownerPage, e := chats.Messages(ctx, f.owner, "sender-view", f.m.ConversationID, chat.Query{Limit: 50})
		must(e)
		if len(ownerPage.Items) != 1 || !ownerPage.Items[0].ViewerIsSender {
			t.Fatal("同条消息未按当前参与者投影本人标记")
		}
		tr := task(a.MessageID)
		detail, e := reviews.Case(ctx, adminToken, "private-case", tr.ID)
		must(e)
		if detail.Body != body || detail.SchoolRelated {
			t.Fatal("私聊原文被公开脱敏或涉校路径污染")
		}
		approved, e := reviews.Decide(ctx, adminToken, uuid.NewString(), "approve", tr.ID, moderation.Decision{Decision: "approved", ExpectedVersion: tr.Version})
		must(e)
		if approved.BusinessOutcome != "delivered" {
			t.Fatal("人工终审未送达", approved)
		}
		next := page(f, 1)
		if len(next.Items) != 1 || next.Items[0].MessageID != a.MessageID || next.Items[0].Body != body || next.NextAfterSeq != 2 {
			t.Fatal("慢审后达丢失或私聊联系方式被改写")
		}
		empty := page(f, 9)
		if len(empty.Items) != 0 || empty.NextAfterSeq != 9 {
			t.Fatal("空页错误推进")
		}
		var audits int64
		must(root.Model(&entity.AccessAudit{}).Where("purpose='moderation.detail' AND subject_ref=?", f.ownerID).Count(&audits).Error)
		if audits != 1 {
			t.Fatal("私聊访问未审计")
		}
		if _, e = chats.MessageStatus(ctx, f.helper, "foreign", a.MessageID); e != command.ErrNotFound {
			t.Fatal("非发送者可查待审状态", e)
		}
	})
	t.Run("客户端不透明标识与同请求幂等", func(t *testing.T) {
		f := matched()
		in := chat.MessageInput{ClientMessageID: "opaque-local-1", Body: strings.Repeat("文", 501), ExpectedWriteEpoch: 1}
		key := uuid.NewString()
		a, e := chats.Send(ctx, f.owner, key, "send", f.m.ConversationID, in)
		must(e)
		b, e := chats.Send(ctx, f.owner, uuid.NewString(), "send", f.m.ConversationID, in)
		must(e)
		if a.MessageID != b.MessageID {
			t.Fatal("客户端标识未去重")
		}
		scan("message", a.MessageID)
		b, e = chats.Send(ctx, f.owner, key, "replay", f.m.ConversationID, in)
		must(e)
		if b.MessageID != a.MessageID || b.DeliverySeq != nil || b.Status != "pending_scan" {
			t.Fatal("接收回执不稳定")
		}
		in.Body = "其他正文"
		if _, e = chats.Send(ctx, f.owner, uuid.NewString(), "different", f.m.ConversationID, in); e != command.ErrIdempotencyConflict {
			t.Fatal("同客户端标识不同请求未拒绝", e)
		}
		for _, body := range []string{"", " \n\u3000", strings.Repeat("a", (64<<10)+1), string([]byte{0xff})} {
			in.ClientMessageID = uuid.NewString()
			in.Body = body
			if _, e = chats.Send(ctx, f.owner, uuid.NewString(), "invalid", f.m.ConversationID, in); e != command.ErrInvalid {
				t.Fatal("无效正文未拒绝", e)
			}
		}
		in.Body = "正常"
		in.ExpectedWriteEpoch = 0
		if _, e = chats.Send(ctx, f.owner, uuid.NewString(), "epoch", f.m.ConversationID, in); e != command.ErrVersionConflict {
			t.Fatal("写代次未复核", e)
		}
		var count int64
		must(root.Model(&entity.Message{}).Where("conversation_id=?", f.m.ConversationID).Count(&count).Error)
		if count != 1 {
			t.Fatal("失败请求产生消息")
		}
	})
	t.Run("送达回滚不占号重试同号", func(t *testing.T) {
		f := matched()
		a := send(f, "事务回滚")
		ev := event("message", a.MessageID)
		must(root.Exec("REVOKE INSERT ON public.notifications FROM liyuanci_domain_worker").Error)
		_, failure := worker.Consume(ctx, ev)
		must(root.Exec("GRANT INSERT ON public.notifications TO liyuanci_domain_worker").Error)
		if failure == nil {
			t.Fatal("未触发通知写入故障")
		}
		var c entity.Conversation
		must(root.Where("id=?", f.m.ConversationID).Take(&c).Error)
		if c.NextDeliverySeq != 1 || message(a.MessageID).DeliverySeq != nil {
			t.Fatal("失败事务消耗送达号")
		}
		must(root.Model(&entity.WorkItem{}).Where("id=?", ev.CorrelationID).Updates(map[string]any{"next_at": time.Now().Add(-time.Second)}).Error)
		out, e := worker.Consume(ctx, ev)
		must(e)
		if !out.Applied || *message(a.MessageID).DeliverySeq != 1 {
			t.Fatal("重试未使用序号1")
		}
		out, e = worker.Consume(ctx, ev)
		must(e)
		if !out.Replayed {
			t.Fatal("重复事件未命中回执")
		}
		var n int64
		must(root.Model(&entity.Notification{}).Where("target_ref=? AND type='message_delivered'", f.m.ConversationID).Count(&n).Error)
		if n != 1 {
			t.Fatal("重复通知")
		}
	})
	t.Run("并发送达连续唯一", func(t *testing.T) {
		f := matched()
		a, b := send(f, "并发甲"), send(f, "并发乙")
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, id := range []string{a.MessageID, b.MessageID} {
			ev := event("message", id)
			wg.Add(1)
			go func() { defer wg.Done(); _, e := worker.Consume(ctx, ev); errs <- e }()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			must(e)
		}
		got := page(f, 0)
		if len(got.Items) != 2 || got.Items[0].DeliverySeq != 1 || got.Items[1].DeliverySeq != 2 {
			t.Fatal("并发序号不连续")
		}
	})
	t.Run("普通驳回危机隔离与无命中不可枚举", func(t *testing.T) {
		f := matched()
		a := send(f, "普通命中")
		scan("message", a.MessageID)
		tr := task(a.MessageID)
		_, e := reviews.Decide(ctx, adminToken, uuid.NewString(), "reject", tr.ID, moderation.Decision{Decision: "rejected", ExpectedVersion: tr.Version, Reason: "合成原因", RevisionSuggestion: "合成修改建议"})
		must(e)
		status, e := chats.MessageStatus(ctx, f.owner, "status", a.MessageID)
		must(e)
		if status.Status != "rejected" || status.DeliverySeq != nil || status.ReviewResult == nil || status.ReviewResult.Reason == nil || status.ReviewResult.RevisionSuggestion == nil {
			t.Fatal("驳回状态缺少独立原因建议")
		}
		for _, body := range []string{"危机命中 普通命中", "无违规联系方式 wx:synthetic"} {
			m := send(f, body)
			scan("message", m.MessageID)
			if _, e = reviews.Case(ctx, adminToken, "hidden", task(m.MessageID).ID); e != command.ErrNotFound {
				t.Fatal("普通审核读取危机或无命中私聊", e)
			}
		}
		cases, e := reviews.Cases(ctx, adminToken, "cases", "", 100)
		must(e)
		for _, c := range cases.Items {
			if c.Kind == "message" {
				var scan entity.ModerationScan
				must(root.Where("task_id=?", c.ID).Take(&scan).Error)
				if scan.CrisisHit || !scan.OrdinaryHit {
					t.Fatal("列表包含非普通命中私聊")
				}
			}
		}
	})
	// 上游完成、拉黑、处罚、注销入口属于后续任务；这里用同锁序事务模拟生效及永久取消。
	transition := func(f fixture, change func(*gorm.DB) error) {
		t.Helper()
		must(root.Transaction(func(db *gorm.DB) error {
			p, e := wish.DiscoverInteraction(ctx, db, f.w.ID)
			if e != nil {
				return e
			}
			_, e = command.LockPlan(ctx, db, p, time.Second)
			if e != nil {
				return e
			}
			if e = change(db); e != nil {
				return e
			}
			var rows []entity.Message
			if e = db.Where("conversation_id=? AND status IN ?", f.m.ConversationID, []string{"pending_scan", "pending_review"}).Find(&rows).Error; e != nil {
				return e
			}
			for _, m := range rows {
				if e = chat.CancelMessage(ctx, db, m); e != nil {
					return e
				}
			}
			return db.Model(&entity.Conversation{}).Where("id=?", f.m.ConversationID).Updates(map[string]any{"write_epoch": gorm.Expr("write_epoch+1"), "version": gorm.Expr("version+1")}).Error
		}))
	}
	t.Run("实现只读与回退不复活", func(t *testing.T) {
		f := matched()
		visible := send(f, "已送达")
		scan("message", visible.MessageID)
		pending := send(f, "普通命中 待审")
		scan("message", pending.MessageID)
		transition(f, func(db *gorm.DB) error {
			return db.Model(&entity.Wish{}).Where("id=?", f.w.ID).Updates(map[string]any{"state": "realized", "version": gorm.Expr("version+1")}).Error
		})
		if len(page(f, 0).Items) != 1 {
			t.Fatal("实现后历史不可读")
		}
		list, e := chats.Conversations(ctx, f.owner, "readonly", chat.Query{Limit: 20})
		must(e)
		if len(list.Items) != 1 || list.Items[0].CanWrite {
			t.Fatal("实现后未只读")
		}
		if _, e = chats.Send(ctx, f.owner, uuid.NewString(), "readonly", f.m.ConversationID, chat.MessageInput{ClientMessageID: "new", Body: "失败", ExpectedWriteEpoch: 2}); e != command.ErrForbidden {
			t.Fatal("实现后仍能写", e)
		}
		var w entity.Wish
		must(root.Where("id=?", f.w.ID).Take(&w).Error)
		_, e = wishes.Rollback(ctx, f.owner, uuid.NewString(), "rollback", w.ID, wish.RollbackInput{ExpectedVersion: w.Version, Target: wish.Matching})
		must(e)
		out, e := worker.Consume(ctx, event("message", pending.MessageID))
		must(e)
		if !out.Cancelled && !out.Replayed {
			t.Fatal("旧审核未终结")
		}
		if message(pending.MessageID).Status != "cancelled" || message(pending.MessageID).DeliverySeq != nil {
			t.Fatal("回退复活旧消息")
		}
	})
	t.Run("新匹配隔离旧会话", func(t *testing.T) {
		f := matched()
		old := send(f, "旧历史")
		scan("message", old.MessageID)
		pending := send(f, "旧待审")
		_, e = wishes.Rollback(ctx, f.owner, uuid.NewString(), "end", f.w.ID, wish.RollbackInput{ExpectedVersion: wishRow(f.w.ID).Version, Target: wish.Published})
		must(e)
		if message(pending.MessageID).Status != "cancelled" {
			t.Fatal("结束未取消待审")
		}
		_, newHelper := student()
		a := apply(newHelper, f.w, "新应愿")
		scan("application", a.ID)
		next := accept(f.owner, f.w, a.ID)
		if next.ConversationID == f.m.ConversationID {
			t.Fatal("重新匹配复用旧会话")
		}
		if _, e = chats.Messages(ctx, newHelper, "foreign-history", f.m.ConversationID, chat.Query{Limit: 50}); e != command.ErrNotFound {
			t.Fatal("新参与者可读旧历史", e)
		}
		if len(page(f, 0).Items) != 1 {
			t.Fatal("旧参与者历史丢失")
		}
	})
	t.Run("已还愿只读", func(t *testing.T) {
		f := matched()
		a := send(f, "历史")
		scan("message", a.MessageID)
		transition(f, func(db *gorm.DB) error {
			return db.Model(&entity.Wish{}).Where("id=?", f.w.ID).Update("state", "fulfilled").Error
		})
		if len(page(f, 0).Items) != 1 {
			t.Fatal("已还愿历史不可读")
		}
		if _, e := chats.Send(ctx, f.owner, uuid.NewString(), "fulfilled", f.m.ConversationID, chat.MessageInput{ClientMessageID: "new", Body: "不应写入", ExpectedWriteEpoch: 2}); e != command.ErrForbidden {
			t.Fatal("已还愿仍可写", e)
		}
	})

	t.Run("注销意图立即阻断历史", func(t *testing.T) {
		f := matched()
		a := send(f, "历史")
		scan("message", a.MessageID)
		transition(f, func(db *gorm.DB) error {
			return db.Model(&entity.Account{}).Where("id=?", f.helperID).Update("lifecycle", "deleting").Error
		})
		if _, e = chats.Messages(ctx, f.owner, "deleted", f.m.ConversationID, chat.Query{Limit: 50}); e != command.ErrNotFound {
			t.Fatal("物理清理暂停时仍可读", e)
		}
		list, e := chats.Conversations(ctx, f.owner, "deleted-list", chat.Query{Limit: 20})
		must(e)
		if len(list.Items) != 0 {
			t.Fatal("注销旧会话仍列出")
		}
	})
	t.Run("限制解除旧消息永久取消", func(t *testing.T) {
		for _, kind := range []string{"block", "penalty", "hold"} {
			t.Run(kind, func(t *testing.T) {
				f := matched()
				a := send(f, "普通命中")
				scan("message", a.MessageID)
				transition(f, func(db *gorm.DB) error {
					switch kind {
					case "block":
						return db.Create(&entity.Block{OwnerID: f.helperID, TargetID: f.ownerID}).Error
					case "penalty":
						starts := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
						ends := starts.Add(168 * time.Hour)
						return db.Create(&entity.Penalty{ID: uuid.NewString(), AccountID: f.helperID, Level: "general", ActionKind: "mute", ViolationRef: uuid.NewString(), ContentVersion: 1, StartsAt: starts, EndsAt: &ends, ReasonRef: uuid.NewString()}).Error
					default:
						return db.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "wish", TargetID: f.w.ID, HoldEpoch: 1, RequiredContentVersion: 2, Status: "active"}).Error
					}
				})
				list, e := chats.Conversations(ctx, f.owner, "restricted", chat.Query{Limit: 20})
				must(e)
				if len(list.Items) != 1 || list.Items[0].CanWrite {
					t.Fatal("限制期间会话可写")
				}
				if _, e = chats.Send(ctx, f.owner, uuid.NewString(), "restricted", f.m.ConversationID, chat.MessageInput{ClientMessageID: "restricted", Body: "新消息", ExpectedWriteEpoch: 2}); e != command.ErrForbidden && e != command.ErrBlockedInteraction {
					t.Fatal("限制期间提交未拒绝", e)
				}
				if _, e = chats.Messages(ctx, f.owner, "history", f.m.ConversationID, chat.Query{Limit: 50}); e != nil {
					t.Fatal("限制不应删除原参与者普通历史", e)
				}
				switch kind {
				case "block":
					must(root.Where("owner_id=?", f.helperID).Delete(&entity.Block{}).Error)
				case "penalty":
					must(root.Model(&entity.Penalty{}).Where("account_id=?", f.helperID).Update("revoked_at", time.Now()).Error)
				default:
					must(root.Model(&entity.PrivacyHold{}).Where("target_id=?", f.w.ID).Updates(map[string]any{"status": "released", "released_at": time.Now()}).Error)
				}
				_, e = worker.Consume(ctx, event("message", a.MessageID))
				must(e)
				if m := message(a.MessageID); m.Status != "cancelled" || m.DeliverySeq != nil {
					t.Fatal("限制解除后复活")
				}
				if _, e = reviews.Case(ctx, adminToken, "obsolete", task(a.MessageID).ID); e != command.ErrNotFound {
					t.Fatal("旧案件仍可访问", e)
				}
			})
		}
	})
	t.Run("扫描期间对方权限变化", func(t *testing.T) {
		f := matched()
		a := send(f, "普通命中 需延迟分析")
		entered, release := make(chan struct{}), make(chan struct{})
		blocked, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{AnalysisTimeout: 5 * time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}, MessageAnalyzer: privateAnalyzer{entered: entered, release: release}})
		must(e)
		result := make(chan error, 1)
		go func() { _, e := blocked.Consume(ctx, event("message", a.MessageID)); result <- e }()
		<-entered
		must(root.Transaction(func(db *gorm.DB) error {
			p, e := chat.DiscoverConversation(ctx, db, f.m.ConversationID)
			if e != nil {
				return e
			}
			_, e = command.LockPlan(ctx, db, p, time.Second)
			if e != nil {
				return e
			}
			return db.Model(&entity.Account{}).Where("id=?", f.helperID).Update("auth_epoch", 2).Error
		}))
		close(release)
		must(<-result)
		if message(a.MessageID).Status != "cancelled" {
			t.Fatal("扫描前快照越过权限撤销")
		}
	})
	t.Run("缺私聊分析器失败关闭", func(t *testing.T) {
		f := matched()
		a := send(f, "普通命中 私聊")
		missing, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{AnalysisTimeout: time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}})
		must(e)
		out, e := missing.Consume(ctx, event("message", a.MessageID))
		if e == nil || out.Applied || message(a.MessageID).Status != "pending_scan" {
			t.Fatal("私聊回退到公开放行器")
		}
	})
	t.Run("通知归属已读版本与游标", func(t *testing.T) {
		f := matched()
		a := send(f, "通知原文不可外泄")
		scan("message", a.MessageID)
		p, e := chats.Notifications(ctx, f.helper, "notifications", chat.Query{Limit: 1})
		must(e)
		if len(p.Items) != 1 || p.Items[0].Read || p.NextCursor == nil {
			t.Fatal("通知列表分页错误")
		}
		second, e := chats.Notifications(ctx, f.helper, "next", chat.Query{Limit: 1, Cursor: *p.NextCursor})
		must(e)
		if len(second.Items) != 1 || second.Items[0].ID == p.Items[0].ID {
			t.Fatal("通知分页重复")
		}
		if _, e = chats.Notifications(ctx, f.owner, "foreign", chat.Query{Limit: 1, Cursor: *p.NextCursor}); e != command.ErrInvalid {
			t.Fatal("游标跨主体可用", e)
		}
		n := p.Items[0]
		key := uuid.NewString()
		read, e := chats.ReadNotification(ctx, f.helper, key, "read", n.ID, n.Version)
		must(e)
		if !read.Read || read.Version != n.Version+1 {
			t.Fatal("已读版本不增")
		}
		replay, e := chats.ReadNotification(ctx, f.helper, key, "read", n.ID, n.Version)
		must(e)
		if replay.Version != read.Version {
			t.Fatal("幂等已读重复增版本")
		}
		if _, e = chats.ReadNotification(ctx, f.helper, uuid.NewString(), "stale", n.ID, n.Version); e != command.ErrVersionConflict {
			t.Fatal("旧版本未拒绝", e)
		}
		if _, e = chats.ReadNotification(ctx, f.owner, uuid.NewString(), "foreign", n.ID, n.Version); e != command.ErrNotFound {
			t.Fatal("可读他人通知", e)
		}
		raw, _ := json.Marshal(p)
		if strings.Contains(string(raw), "通知原文") || strings.Contains(string(raw), f.ownerID) {
			t.Fatal("通知泄漏正文身份")
		}
	})
	t.Run("HTTP六接口与严格输入", func(t *testing.T) {
		f := matched()
		r := httpapi.New(logger, nil)
		must(httpapi.RegisterChat(r, chats))
		request := func(method, path, token, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", uuid.NewString())
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			return out
		}
		path := "/api/v1/conversations/" + f.m.ConversationID + "/messages"
		for _, body := range []string{`{"client_message_id":"x","body":"x"}`, `{"client_message_id":"x","body":null,"expected_write_epoch":1}`, `{"client_message_id":"x","body":"x","body":"y","expected_write_epoch":1}`, `{"client_message_id":"x","Body":"x","expected_write_epoch":1}`, `{"client_message_id":"x","body":"x","expected_write_epoch":1,"delivery_seq":1}`} {
			if out := request("POST", path, f.owner, body); out.Code != 400 {
				t.Fatal("非法字段未拒绝", out.Code)
			}
		}
		out := request("POST", path, f.owner, `{"client_message_id":"http-opaque","body":"HTTP消息","expected_write_epoch":1}`)
		if out.Code != 202 || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("提交HTTP失败", out.Code, out.Body.String())
		}
		var payload struct {
			Data chat.Accepted `json:"data"`
		}
		must(json.Unmarshal(out.Body.Bytes(), &payload))
		for _, url := range []string{"/api/v1/me/conversations", path, "/api/v1/me/messages/" + payload.Data.MessageID + "/status", "/api/v1/me/notifications"} {
			if got := request("GET", url, f.owner, ""); got.Code != 200 {
				t.Fatal("查询HTTP失败", url, got.Code)
			}
		}
		for _, query := range []string{"?after_seq=1&cursor=x", "?after_seq=-1", "?limit=51", "?limit=1&limit=2", "?unknown=1"} {
			if got := request("GET", path+query, f.helper, ""); got.Code != 400 {
				t.Fatal("非法增量参数未拒绝", query, got.Code)
			}
		}
		p, e := chats.Notifications(ctx, f.helper, "http-notification", chat.Query{Limit: 20})
		must(e)
		if got := request("POST", "/api/v1/me/notifications/"+p.Items[0].ID+"/read", f.helper, `{"expected_version":1}`); got.Code != 200 {
			t.Fatal("已读HTTP失败", got.Code)
		}
	})
	if nameserver := os.Getenv("APP_MODERATION_TEST_NAMESERVER"); nameserver != "" {
		t.Run("真实MQ私聊提交后NACK重投同号", func(t *testing.T) {
			f := matched()
			a := send(f, "真实MQ私聊")
			ev := event("message", a.MessageID)
			unique := "chat_" + uuid.NewString()[:8]
			cfg := transportmq.Config{NameServers: []string{nameserver}, Topic: "liyuanci_content_review_v1", EventType: wish.ReviewRoute().EventType, ProducerGroup: unique + "_p", ConsumerGroup: unique + "_c", Instance: unique, AllowUnauthenticated: true, SendTimeout: 3 * time.Second, HandlerTimeout: 30 * time.Second, Concurrency: 1, MaxReconsume: 3, RetryDelayLevel: 1}
			must(transportmq.ConfigureSDKLogging(logger))
			sender, e := transportmq.OpenSender(ctx, cfg, logger)
			must(e)
			defer sender.Close()
			h := &handler{worker: worker, seen: make(chan durable.Consumption, 512)}
			sub, e := transportmq.OpenSubscriber(ctx, cfg, h, logger)
			must(e)
			defer sub.Close()
			must(sender.Send(ctx, ev))
			timer := time.NewTimer(70 * time.Second)
			defer timer.Stop()
			applied, replayed := false, false
			for !applied || !replayed {
				select {
				case out := <-h.seen:
					applied = applied || out.Applied
					replayed = replayed || out.Replayed
				case <-timer.C:
					t.Fatal("MQ私聊重投超时")
				}
			}
			if m := message(a.MessageID); m.Status != "delivered" || m.DeliverySeq == nil || *m.DeliverySeq != 1 {
				t.Fatal("MQ未送达同号")
			}
			var n int64
			must(root.Model(&entity.Notification{}).Where("type='message_delivered' AND target_ref=?", f.m.ConversationID).Count(&n).Error)
			if n != 1 {
				t.Fatal("MQ重复通知")
			}
		})
	}
	var missing int64
	must(root.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname IN ('public','evidence') AND c.relkind='r' AND (coalesce(obj_description(c.oid,'pg_class'),'')='' OR coalesce(col_description(c.oid,a.attnum),'')='')`).Scan(&missing).Error)
	if missing != 0 {
		t.Fatal("数据库对象注释缺失")
	}
	if !t.Failed() {
		t.Log("T021真实PG/HTTP/MQ聊天、通知与最终送达序号通过；身份、密钥、分类器及后续状态入口为合成")
	}
}

type privateAnalyzer struct {
	entered chan struct{}
	release chan struct{}
}

func (a privateAnalyzer) AnalyzeMessage(ctx context.Context, _ string) (moderation.MessageAssessment, error) {
	if a.entered != nil {
		close(a.entered)
		select {
		case <-a.release:
		case <-ctx.Done():
			return moderation.MessageAssessment{}, ctx.Err()
		}
	}
	return moderation.MessageAssessment{DetectorVersion: "synthetic-private-v1"}, nil
}
