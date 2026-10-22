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

// TestApplicationMatching 覆盖T019、REQ-004/005/006/007/015/018的真实PG事务与HTTP。
// 身份、密钥、词库及文本分析均合成；已实现起点由夹具准备，不宣称完成T023。
func TestApplicationMatching(t *testing.T) {
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
	database := "applications_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	worker, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{AnalysisTimeout: 5 * time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}})
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
	checkCounts := func(id string, matches, conversations int64) {
		t.Helper()
		var n int64
		must(root.Model(&entity.Match{}).Where("wish_id=? AND status='current'", id).Count(&n).Error)
		if n != matches {
			t.Fatal("当前匹配数量错误", n)
		}
		must(root.Model(&entity.Conversation{}).Table("public.conversations AS c").Joins("JOIN public.matches m ON m.id=c.match_id").Where("m.wish_id=?", id).Count(&n).Error)
		if n != conversations {
			t.Fatal("会话数量错误", n)
		}
	}
	t.Run("留言码点与本人资格状态", func(t *testing.T) {
		owner, token := student()
		_, applicant := student()
		w := published(token)
		if _, e = wishes.Apply(ctx, token, uuid.NewString(), "self", w.ID, wish.ApplicationInput{Message: "自应愿", WishContentVersion: 1}); e != command.ErrForbidden {
			t.Fatal("自应愿未拒绝", e)
		}
		if _, e = wishes.Apply(ctx, applicant, uuid.NewString(), "too-long", w.ID, wish.ApplicationInput{Message: strings.Repeat("鲤", 101), WishContentVersion: 1}); e != command.ErrInvalid {
			t.Fatal("101码点未拒绝", e)
		}
		a := apply(applicant, w, strings.Repeat("鲤", 100))
		if a.Status != "pending_scan" {
			t.Fatal("未先持久待审")
		}
		if _, e = wishes.Apply(ctx, applicant, uuid.NewString(), "duplicate", w.ID, wish.ApplicationInput{Message: "重复", WishContentVersion: 1}); e != command.ErrDuplicateApplication {
			t.Fatal("重复未拒绝", e)
		}
		scan("application", a.ID)
		page, e := wishes.Applications(ctx, token, "active", w.ID, wish.ApplicationQuery{Limit: 20})
		must(e)
		if len(page.Items) != 1 || page.Items[0].Message == nil || len([]rune(*page.Items[0].Message)) != 100 {
			t.Fatal("100码点审核投影错误")
		}
		if strings.Contains(string(appRow(a.ID).MessageCipher), "鲤") {
			t.Fatal("数据库出现明文")
		}
		must(root.Model(&entity.Account{}).Where("id=?", owner).Update("verification", "unverified").Error)
		if _, e = wishes.AcceptApplication(ctx, token, uuid.NewString(), "revoked", a.ID, wish.AcceptInput{ExpectedVersion: 2, WishExpectedVersion: w.Version}); e != command.ErrForbidden {
			t.Fatal("撤销资格后仍可同意", e)
		}
	})
	t.Run("待审隐藏及幂等重放撤销重申", func(t *testing.T) {
		_, owner := student()
		_, applicant := student()
		w := published(owner)
		key := uuid.NewString()
		in := wish.ApplicationInput{Message: "待审秘密", WishContentVersion: 1}
		a, e := wishes.Apply(ctx, applicant, key, "first", w.ID, in)
		must(e)
		replay, e := wishes.Apply(ctx, applicant, key, "replay", w.ID, in)
		must(e)
		if a.ID != replay.ID {
			t.Fatal("重放新建申请")
		}
		_, e = wishes.Apply(ctx, applicant, key, "different", w.ID, wish.ApplicationInput{Message: "不同", WishContentVersion: 1})
		if e != command.ErrIdempotencyConflict {
			t.Fatal("同键异请求未拒绝", e)
		}
		page, e := wishes.Applications(ctx, owner, "hidden", w.ID, wish.ApplicationQuery{Limit: 20})
		must(e)
		if len(page.Items) != 0 {
			t.Fatal("待审泄漏许愿人")
		}
		_, e = wishes.AcceptApplication(ctx, owner, uuid.NewString(), "pending", a.ID, wish.AcceptInput{ExpectedVersion: 1, WishExpectedVersion: w.Version})
		if e != command.ErrReviewPending {
			t.Fatal("待审可同意", e)
		}
		old := event("application", a.ID)
		cancelled, e := wishes.WithdrawApplication(ctx, applicant, uuid.NewString(), "withdraw", a.ID, 1)
		must(e)
		if cancelled.Status != "withdrawn" {
			t.Fatal("未撤销")
		}
		out, e := worker.Consume(ctx, old)
		must(e)
		if out.Applied || appRow(a.ID).Status != "withdrawn" {
			t.Fatal("旧审核恢复撤销申请")
		}
		again := apply(applicant, w, "")
		scan("application", again.ID)
		if appRow(again.ID).Status != "active" {
			t.Fatal("空留言或重申失败")
		}
	})
	t.Run("人工驳回建议与批准通知", func(t *testing.T) {
		_, owner := student()
		_, applicant := student()
		w := published(owner)
		a := apply(applicant, w, "普通命中留言")
		scan("application", a.ID)
		row := appRow(a.ID)
		if row.Status != "pending_review" {
			t.Fatal("命中未进入人工")
		}
		task := taskRow(*row.ReviewTaskID)
		detail, e := reviews.Case(ctx, adminToken, "detail", task.ID)
		must(e)
		if detail.Body != "普通命中留言" {
			t.Fatal("案件原文不匹配")
		}
		out, e := reviews.Decide(ctx, adminToken, uuid.NewString(), "reject", task.ID, moderation.Decision{Decision: "rejected", Reason: "合成驳回原因", RevisionSuggestion: "合成修改建议", ExpectedVersion: task.Version})
		must(e)
		if out.ContentDecision != "rejected" {
			t.Fatal("驳回失败")
		}
		own, e := wishes.OwnApplications(ctx, applicant, "own", wish.ApplicationQuery{Limit: 20})
		must(e)
		if len(own.Items) != 1 || own.Items[0].ReviewResult == nil || own.Items[0].ReviewResult.Reason == nil || own.Items[0].ReviewResult.RevisionSuggestion == nil {
			t.Fatal("本人缺驳回建议")
		}
		a = apply(applicant, w, "学校命中留言")
		scan("application", a.ID)
		row = appRow(a.ID)
		task = taskRow(*row.ReviewTaskID)
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "approve", task.ID, moderation.Decision{Decision: "approved", ExpectedVersion: task.Version})
		must(e)
		if appRow(a.ID).Status != "active" {
			t.Fatal("人工批准未生效")
		}
		var n int64
		must(root.Model(&entity.Notification{}).Where("target_ref=? AND type='application_active'", a.ID).Count(&n).Error)
		if n != 1 {
			t.Fatal("站内兜底通知不唯一")
		}
	})
	t.Run("资格拉黑到期及改版取消晚到", func(t *testing.T) {
		for _, mode := range []string{"block", "penalty", "expired", "content", "deleting", "hold"} {
			t.Run(mode, func(t *testing.T) {
				ownerID, owner := student()
				applicantID, applicant := student()
				w := published(owner)
				a := apply(applicant, w, "晚到合成留言")
				switch mode {
				case "block":
					must(root.Create(&entity.Block{OwnerID: ownerID, TargetID: applicantID}).Error)
				case "penalty":
					starts := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
					ends := starts.Add(168 * time.Hour)
					must(root.Create(&entity.Penalty{ID: uuid.NewString(), AccountID: applicantID, Level: "general", ActionKind: "mute", ViolationRef: uuid.NewString(), ContentVersion: 1, StartsAt: starts, EndsAt: &ends, ReasonRef: uuid.NewString()}).Error)
				case "expired":
					must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Update("expires_at", time.Now().Add(-time.Second)).Error)
				case "content":
					_, e = wishes.Update(ctx, owner, uuid.NewString(), "edit", w.ID, wish.UpdateInput{ContentInput: wish.ContentInput{Category: "help", Campus: "wushan", Body: "变更版本"}, ExpectedVersion: w.Version})
					must(e)
				case "deleting":
					must(root.Model(&entity.Account{}).Where("id=?", applicantID).Update("lifecycle", "deleting").Error)
				case "hold":
					must(root.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "wish", TargetID: w.ID, HoldEpoch: 1, RequiredContentVersion: 2}).Error)
				}
				out, e := worker.Consume(ctx, event("application", a.ID))
				must(e)
				if out.Applied || appRow(a.ID).Status != "invalidated" {
					t.Fatal("晚到旧审核恢复交互", mode)
				}
				if mode == "block" {
					must(root.Where("owner_id=? AND target_id=?", ownerID, applicantID).Delete(&entity.Block{}).Error)
					_, e = worker.Consume(ctx, event("application", a.ID))
					must(e)
					if appRow(a.ID).Status != "invalidated" {
						t.Fatal("解除拉黑恢复旧申请")
					}
				}
			})
		}
	})
	t.Run("并发申请同意与匹配隔离", func(t *testing.T) {
		_, owner := student()
		_, aToken := student()
		_, bToken := student()
		w := published(owner)
		a := apply(aToken, w, "甲留言")
		b := apply(bToken, w, "乙留言")
		scan("application", a.ID)
		scan("application", b.ID)
		results := make(chan error, 2)
		var group sync.WaitGroup
		for _, id := range []string{a.ID, b.ID} {
			group.Add(1)
			go func(id string) {
				defer group.Done()
				_, e := wishes.AcceptApplication(ctx, owner, uuid.NewString(), "concurrent", id, wish.AcceptInput{ExpectedVersion: 2, WishExpectedVersion: w.Version})
				results <- e
			}(id)
		}
		group.Wait()
		close(results)
		success := 0
		for e := range results {
			if e == nil {
				success++
			} else if e != command.ErrVersionConflict && e != command.ErrConflict && e != command.ErrRetry {
				t.Fatal(e)
			}
		}
		if success != 1 {
			t.Fatal("并发同意未形成唯一人", success)
		}
		checkCounts(w.ID, 1, 1)
		if _, e = wishes.Apply(ctx, bToken, uuid.NewString(), "new-while-matching", w.ID, wish.ApplicationInput{Message: "新申请", WishContentVersion: 1}); e != command.ErrConflict {
			t.Fatal("应愿中仍接受新申请", e)
		}
		var m entity.Match
		must(root.Where("wish_id=? AND status='current'", w.ID).Take(&m).Error)
		token, other := aToken, bToken
		if m.ApplicationID != nil && *m.ApplicationID == b.ID {
			token, other = bToken, aToken
		}
		detail, e := wishes.Detail(ctx, token, "participant", w.ID)
		must(e)
		if detail.Participant == nil || detail.Public != nil || detail.OwnContent != nil {
			t.Fatal("匹配视图错误")
		}
		_, e = wishes.Detail(ctx, other, "other", w.ID)
		if e != command.ErrNotFound {
			t.Fatal("未匹配方读取私有详情", e)
		}
	})
	t.Run("匹配重放不重复会话通知及结束后拒绝", func(t *testing.T) {
		_, owner := student()
		_, applicant := student()
		w := published(owner)
		a := apply(applicant, w, "匹配留言")
		scan("application", a.ID)
		key := uuid.NewString()
		in := wish.AcceptInput{ExpectedVersion: 2, WishExpectedVersion: w.Version}
		first, e := wishes.AcceptApplication(ctx, owner, key, "accept", a.ID, in)
		must(e)
		second, e := wishes.AcceptApplication(ctx, owner, key, "replay", a.ID, in)
		must(e)
		if first != second {
			t.Fatal("匹配重放返回其他关系")
		}
		checkCounts(w.ID, 1, 1)
		var n int64
		must(root.Model(&entity.Notification{}).Where("target_ref=? AND type='application_accepted'", first.MatchID).Count(&n).Error)
		if n != 1 {
			t.Fatal("重放重复通知")
		}
		row := wishRow(w.ID)
		_, e = wishes.Rollback(ctx, owner, uuid.NewString(), "rollback", w.ID, wish.RollbackInput{ExpectedVersion: row.Version, Target: wish.Published})
		must(e)
		_, e = wishes.AcceptApplication(ctx, owner, key, "ended", a.ID, in)
		if e == nil {
			t.Fatal("旧回执把已结束匹配当当前关系")
		}
	})
	t.Run("回退过期刷新与重匹配新会话", func(t *testing.T) {
		for _, refresh := range []bool{false, true} {
			t.Run(map[bool]string{false: "保留期限", true: "刷新重审"}[refresh], func(t *testing.T) {
				_, owner := student()
				_, applicant := student()
				w := published(owner)
				a := apply(applicant, w, "匹配留言")
				scan("application", a.ID)
				m := accept(owner, w, a.ID)
				expired := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
				must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Update("expires_at", expired).Error)
				row := wishRow(w.ID)
				out, e := wishes.Rollback(ctx, owner, uuid.NewString(), "rollback", w.ID, wish.RollbackInput{ExpectedVersion: row.Version, Target: wish.Published, RefreshExpiry: refresh})
				must(e)
				if refresh {
					if out.State != wish.PendingReview || out.ContentVersion != 2 {
						t.Fatal("刷新沿用旧审核")
					}
					scan("wish", w.ID)
					row = wishRow(w.ID)
					out, e = wishes.Publish(ctx, owner, uuid.NewString(), "publish-again", w.ID, row.Version)
					must(e)
					if out.State != wish.Published || !wishRow(w.ID).ExpiresAt.After(time.Now()) {
						t.Fatal("新上架未重算期限")
					}
					w = out
					a = apply(applicant, w, "重新申请")
					scan("application", a.ID)
					newMatch := accept(owner, w, a.ID)
					if m.MatchID == newMatch.MatchID || m.ConversationID == newMatch.ConversationID {
						t.Fatal("重匹配复用了旧会话")
					}
					checkCounts(w.ID, 1, 2)
				} else if out.State != wish.Expired || !wishRow(w.ID).ExpiresAt.Equal(expired) {
					t.Fatal("不刷新把过期心愿重新公开")
				}
				var old entity.Match
				must(root.Where("id=?", m.MatchID).Take(&old).Error)
				if old.Status != "ended" || appRow(a.ID).Status == "accepted" && !refresh {
					t.Fatal("回退未解绑")
				}
			})
		}
	})
	t.Run("满三条已实现回退拒绝释放后恢复原会话", func(t *testing.T) {
		ownerID, owner := student()
		_, applicant := student()
		w := published(owner)
		a := apply(applicant, w, "匹配留言")
		scan("application", a.ID)
		m := accept(owner, w, a.ID)
		// T023前置状态由合成夹具准备，只验收T019从已实现回退的事务。
		must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Updates(map[string]any{"state": "realized", "version": gorm.Expr("version+1")}).Error)
		var before entity.Conversation
		must(root.Where("id=?", m.ConversationID).Take(&before).Error)
		var original entity.Match
		must(root.Where("id=?", m.MatchID).Take(&original).Error)
		messageID := uuid.NewString()
		must(root.Create(&entity.Message{ID: messageID, ConversationID: m.ConversationID, SenderID: original.HelperID, ClientMessageID: uuid.NewString(), BodyCipher: json.RawMessage(`{}`), CancelEpoch: before.WriteEpoch}).Error)
		others := []wish.CreatedDraft{published(owner), published(owner), published(owner)}
		row := wishRow(w.ID)
		in := wish.RollbackInput{ExpectedVersion: row.Version, Target: wish.Matching}
		_, e = wishes.Rollback(ctx, owner, "quota-retry", "full", w.ID, in)
		if e != command.ErrActiveWishLimit {
			t.Fatal("满额未拒绝", e)
		}
		if wishRow(w.ID).State != "realized" {
			t.Fatal("失败改变已实现状态")
		}
		_, e = wishes.Withdraw(ctx, owner, uuid.NewString(), "release", others[0].ID, others[0].Version)
		must(e)
		out, e := wishes.Rollback(ctx, owner, "quota-retry", "retry", w.ID, in)
		must(e)
		if out.State != wish.Matching {
			t.Fatal("释放后回退失败")
		}
		var after entity.Conversation
		must(root.Where("id=?", m.ConversationID).Take(&after).Error)
		var cancelled entity.Message
		must(root.Where("id=?", messageID).Take(&cancelled).Error)
		if after.WriteEpoch != before.WriteEpoch+1 || after.NextDeliverySeq != before.NextDeliverySeq || cancelled.Status != "cancelled" || cancelled.DeliverySeq != nil {
			t.Fatal("回退重放旧消息或改变送达序号")
		}
		checkCounts(w.ID, 1, 1)
		var current entity.Match
		must(root.Where("id=? AND status='current'", m.MatchID).Take(&current).Error)
		var n int64
		must(root.Model(&entity.Wish{}).Where("owner_id=? AND (state='matching' OR (state='published' AND expires_at>clock_timestamp()))", ownerID).Count(&n).Error)
		if n != 3 {
			t.Fatal("活跃额度错误", n)
		}
		must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Update("state", "fulfilled").Error)
		_, e = wishes.Rollback(ctx, owner, uuid.NewString(), "fulfilled", w.ID, wish.RollbackInput{ExpectedVersion: out.Version, Target: wish.Matching})
		if e != command.ErrConflict {
			t.Fatal("已还愿可回退", e)
		}
	})
	t.Run("同人并发申请与跨主体游标拒绝", func(t *testing.T) {
		_, owner := student()
		_, applicant := student()
		_, other := student()
		w := published(owner)
		results := make(chan error, 2)
		var group sync.WaitGroup
		for i := 0; i < 2; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				_, e := wishes.Apply(ctx, applicant, uuid.NewString(), "concurrent-create", w.ID, wish.ApplicationInput{Message: "并发申请", WishContentVersion: 1})
				results <- e
			}()
		}
		group.Wait()
		close(results)
		success, duplicate := 0, 0
		for e := range results {
			if e == nil {
				success++
			} else if e == command.ErrDuplicateApplication {
				duplicate++
			} else {
				t.Fatal(e)
			}
		}
		if success != 1 || duplicate != 1 {
			t.Fatal("同人并发申请去重错误")
		}
		own, e := wishes.OwnApplications(ctx, applicant, "page", wish.ApplicationQuery{Limit: 20})
		must(e)
		if len(own.Items) != 1 {
			t.Fatal("创建了多条有效申请")
		}
		scan("application", own.Items[0].ID)
		b := apply(other, w, "另一申请")
		scan("application", b.ID)
		page, e := wishes.Applications(ctx, owner, "page", w.ID, wish.ApplicationQuery{Limit: 1})
		must(e)
		if len(page.Items) != 1 || page.NextCursor == nil {
			t.Fatal("有效申请游标错误")
		}
		next, e := wishes.Applications(ctx, owner, "page-next", w.ID, wish.ApplicationQuery{Limit: 1, Cursor: *page.NextCursor})
		must(e)
		if len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID {
			t.Fatal("翻页重复或漏读")
		}
		_, e = wishes.OwnApplications(ctx, applicant, "wrong-scope", wish.ApplicationQuery{Limit: 1, Cursor: *page.NextCursor})
		if e != command.ErrInvalid {
			t.Fatal("跨用途游标未拒绝", e)
		}
	})
	t.Run("回退与上架并发不超过三条", func(t *testing.T) {
		ownerID, owner := student()
		_, applicant := student()
		w := published(owner)
		a := apply(applicant, w, "匹配留言")
		scan("application", a.ID)
		accept(owner, w, a.ID)
		must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Updates(map[string]any{"state": "realized", "version": gorm.Expr("version+1")}).Error)
		published(owner)
		published(owner)
		draft, e := wishes.Create(ctx, owner, uuid.NewString(), "capacity-create", wish.ContentInput{Category: "help", Campus: "wushan", Body: "并发上架心愿"})
		must(e)
		draft, e = wishes.Submit(ctx, owner, uuid.NewString(), "capacity-submit", draft.ID, draft.Version, draft.ContentVersion)
		must(e)
		scan("wish", draft.ID)
		original, ready := wishRow(w.ID), wishRow(draft.ID)
		failures := make(chan error, 2)
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			_, e := wishes.Rollback(ctx, owner, uuid.NewString(), "concurrent-rollback", w.ID, wish.RollbackInput{ExpectedVersion: original.Version, Target: wish.Matching})
			failures <- e
		}()
		go func() {
			defer group.Done()
			_, e := wishes.Publish(ctx, owner, uuid.NewString(), "concurrent-publish", draft.ID, ready.Version)
			failures <- e
		}()
		group.Wait()
		close(failures)
		for e := range failures {
			if e != nil && e != command.ErrActiveWishLimit {
				t.Fatal(e)
			}
		}
		var n int64
		must(root.Model(&entity.Wish{}).Where("owner_id=? AND (state='matching' OR (state='published' AND expires_at>clock_timestamp()))", ownerID).Count(&n).Error)
		if n != 3 {
			t.Fatal("并发占用额度错误", n)
		}
	})
	t.Run("拉黑事务与同意并发及解除后不复活", func(t *testing.T) {
		ownerID, owner := student()
		helperID, applicant := student()
		w := published(owner)
		a := apply(applicant, w, "并发拉黑留言")
		scan("application", a.ID)
		locked, attempted := make(chan struct{}), make(chan struct{})
		blockedResult, acceptedResult := make(chan error, 1), make(chan error, 1)
		// T026尚未交付；夹具按统一锁序模拟上游拉黑生效事务，不冒充拉黑HTTP接口验收。
		go func() {
			blockedResult <- root.WithContext(ctx).Transaction(func(db *gorm.DB) error {
				plan, e := wish.DiscoverInteraction(ctx, db, w.ID)
				if e != nil {
					return e
				}
				if _, e = command.LockPlan(ctx, db, plan, time.Second); e != nil {
					return e
				}
				close(locked)
				select {
				case <-attempted:
				case <-time.After(5 * time.Second):
					return command.ErrRetry
				}
				if e = db.Create(&entity.Block{OwnerID: helperID, TargetID: ownerID}).Error; e != nil {
					return e
				}
				var row entity.Application
				if e = db.Where("id=?", a.ID).Take(&row).Error; e != nil {
					return e
				}
				return wish.CancelApplication(ctx, db, row, "invalidated", "blocked_interaction")
			})
		}()
		select {
		case <-locked:
		case e := <-blockedResult:
			t.Fatal("拉黑夹具未取得统一锁", e)
		case <-time.After(5 * time.Second):
			t.Fatal("拉黑夹具超时")
		}
		go func() {
			close(attempted)
			_, e := wishes.AcceptApplication(ctx, owner, uuid.NewString(), "blocked-accept", a.ID, wish.AcceptInput{ExpectedVersion: 2, WishExpectedVersion: w.Version})
			acceptedResult <- e
		}()
		must(<-blockedResult)
		if e := <-acceptedResult; e != command.ErrBlockedInteraction {
			t.Fatal("拉黑生效仍建立新匹配", e)
		}
		checkCounts(w.ID, 0, 0)
		must(root.Where("owner_id=? AND target_id=?", helperID, ownerID).Delete(&entity.Block{}).Error)
		_, e := wishes.AcceptApplication(ctx, owner, uuid.NewString(), "unblocked-old", a.ID, wish.AcceptInput{ExpectedVersion: appRow(a.ID).Version, WishExpectedVersion: w.Version})
		if e != command.ErrConflict {
			t.Fatal("解除拉黑复活旧申请", e)
		}
		fresh := apply(applicant, w, "解除后重新申请")
		scan("application", fresh.ID)
		accept(owner, w, fresh.ID)
	})
	t.Run("通知失败回滚整个同意事务", func(t *testing.T) {
		_, owner := student()
		_, applicant := student()
		w := published(owner)
		a := apply(applicant, w, "事务留言")
		scan("application", a.ID)
		must(root.Exec("REVOKE INSERT ON public.notifications FROM liyuanci_api").Error)
		_, e = wishes.AcceptApplication(ctx, owner, uuid.NewString(), "notification-failure", a.ID, wish.AcceptInput{ExpectedVersion: 2, WishExpectedVersion: w.Version})
		must(root.Exec("GRANT INSERT ON public.notifications TO liyuanci_api").Error)
		if e == nil || appRow(a.ID).Status != "active" || wishRow(w.ID).State != "published" {
			t.Fatal("失败残留业务结果", e)
		}
		checkCounts(w.ID, 0, 0)
		accept(owner, w, a.ID)
	})
	t.Run("真实HTTP六路由及严格输入", func(t *testing.T) {
		_, owner := student()
		_, applicant := student()
		w := published(owner)
		router := httpapi.New(logger, nil)
		must(httpapi.RegisterWishes(router, wishes))
		must(httpapi.RegisterApplications(router, wishes))
		request := func(method, path, token, body string) *httptest.ResponseRecorder {
			t.Helper()
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Idempotency-Key", uuid.NewString())
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			return out
		}
		for _, body := range []string{`{"message":"x","wish_content_version":1,"status":"active"}`, `{"message":"x","message":"y","wish_content_version":1}`, `{"message":null,"wish_content_version":1}`, `{"Message":"x","wish_content_version":1}`} {
			if got := request("POST", "/api/v1/wishes/"+w.ID+"/applications", applicant, body); got.Code != 400 {
				t.Fatal("非法字段未拒绝", got.Code)
			}
		}
		got := request("POST", "/api/v1/wishes/"+w.ID+"/applications", applicant, `{"message":"HTTP留言","wish_content_version":1}`)
		if got.Code != 202 || got.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("提交HTTP失败", got.Code)
		}
		var payload struct {
			Data wish.ApplicationResult `json:"data"`
		}
		must(json.Unmarshal(got.Body.Bytes(), &payload))
		if got := request("GET", "/api/v1/me/applications", applicant, ""); got.Code != 200 {
			t.Fatal("本人列表失败", got.Code)
		}
		if got := request("GET", "/api/v1/wishes/"+w.ID+"/applications", owner, ""); got.Code != 200 || strings.Contains(got.Body.String(), "HTTP留言") {
			t.Fatal("许愿人待审泄漏")
		}
		id := payload.Data.ID
		if got := request("POST", "/api/v1/applications/"+id+"/withdraw", applicant, `{"expected_version":1}`); got.Code != 200 {
			t.Fatal("撤销HTTP失败", got.Code)
		}
		a := apply(applicant, w, "新HTTP留言")
		scan("application", a.ID)
		body, _ := json.Marshal(wish.AcceptInput{ExpectedVersion: 2, WishExpectedVersion: w.Version})
		if got := request("POST", "/api/v1/applications/"+a.ID+"/accept", owner, string(body)); got.Code != 200 {
			t.Fatal("同意HTTP失败", got.Code)
		}
		body, _ = json.Marshal(wish.RollbackInput{ExpectedVersion: wishRow(w.ID).Version, Target: wish.Published})
		if got := request("POST", "/api/v1/wishes/"+w.ID+"/rollback", owner, string(body)); got.Code != 200 {
			t.Fatal("回退HTTP失败", got.Code)
		}
	})
	if nameserver := os.Getenv("APP_MODERATION_TEST_NAMESERVER"); nameserver != "" {
		t.Run("真实MQ应愿扫描重复回执", func(t *testing.T) {
			_, owner := student()
			_, applicant := student()
			w := published(owner)
			a := apply(applicant, w, "MQ应愿留言")
			ev := event("application", a.ID)
			unique := "app_" + uuid.NewString()[:8]
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
					t.Fatal("真实MQ应愿重投超时")
				}
			}
			if appRow(a.ID).Status != "active" {
				t.Fatal("MQ扫描未生效")
			}
			var n int64
			must(root.Model(&entity.Notification{}).Where("target_ref=?", a.ID).Count(&n).Error)
			if n != 1 {
				t.Fatal("重复消费重复通知")
			}
		})
	}
	var missing int64
	must(root.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname IN ('public','evidence') AND c.relkind='r' AND (coalesce(obj_description(c.oid,'pg_class'),'')='' OR coalesce(col_description(c.oid,a.attnum),'')='')`).Scan(&missing).Error)
	if missing != 0 {
		t.Fatal("数据库对象缺中文注释")
	}
	if !t.Failed() {
		t.Log("T019真实PG/HTTP/MQ应愿审核、唯一匹配、回退与B01通过；供应商、身份及已实现起点均合成")
	}
}
