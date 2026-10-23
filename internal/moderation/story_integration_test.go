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

// TestStorySharing 覆盖T023、REQ-004/007/008/015/018的真实PG、HTTP和MQ。
// 身份、密钥、词库及文本分析均合成；确认实现走正式服务，注销生效由同锁序夹具模拟。
func TestStorySharing(t *testing.T) {
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
	database := "stories_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	page := func(f fixture, after int64) chat.MessagesPage {
		t.Helper()
		out, e := chats.Messages(ctx, f.helper, "read", f.m.ConversationID, chat.Query{AfterSeq: &after, Limit: 50})
		must(e)
		return out
	}
	storyVersion := func(id string) entity.StoryVersion {
		t.Helper()
		var v entity.StoryVersion
		must(root.Where("wish_id=? AND story_version=?", id, wishRow(id).CurrentStoryVersion).Take(&v).Error)
		return v
	}
	storyTask := func(id string) entity.ModerationTask { return taskRow(*storyVersion(id).ReviewTaskID) }
	complete := func(f fixture) wish.CreatedDraft {
		t.Helper()
		out, e := wishes.Complete(ctx, f.owner, uuid.NewString(), "complete", f.w.ID, wishRow(f.w.ID).Version)
		must(e)
		if out.State != wish.Realized {
			t.Fatal("完成未进入realized")
		}
		return out
	}
	submit := func(f fixture, body string) wish.StoryAccepted {
		t.Helper()
		out, e := wishes.SubmitStory(ctx, f.owner, uuid.NewString(), "story", f.w.ID, wish.StoryInput{Body: body, WishExpectedVersion: wishRow(f.w.ID).Version})
		must(e)
		if out.Status != "pending_review" || out.WishState != wish.Fulfilled {
			t.Fatal("还愿未先持久化fulfilled")
		}
		return out
	}
	update := func(f fixture, body string) wish.StoryAccepted {
		t.Helper()
		out, e := wishes.UpdateStory(ctx, f.owner, uuid.NewString(), "edit-story", f.w.ID, wish.StoryUpdate{Body: body, ExpectedVersion: wishRow(f.w.ID).Version})
		must(e)
		return out
	}
	invisible := func(id string) {
		t.Helper()
		if _, e := wishes.Story(ctx, "", "invisible", id); e != command.ErrNotFound {
			t.Fatal("非公开故事可读", e)
		}
	}
	approve := func(id string) {
		t.Helper()
		tr := storyTask(id)
		out, e := reviews.Decide(ctx, adminToken, uuid.NewString(), "approve-story", tr.ID, moderation.Decision{Decision: "approved", ExpectedVersion: tr.Version})
		must(e)
		if out.BusinessOutcome != "published" {
			t.Fatal("故事批准未公开", out)
		}
	}
	t.Run("确认实现原子取消与普通回退保留原人", func(t *testing.T) {
		f := matched()
		a := send(f, "普通命中 待审")
		scan("message", a.MessageID)
		version := wishRow(f.w.ID).Version
		key := uuid.NewString()
		done, e := wishes.Complete(ctx, f.owner, key, "complete", f.w.ID, version)
		must(e)
		if done.State != wish.Realized || message(a.MessageID).Status != "cancelled" {
			t.Fatal("完成与取消不原子")
		}
		again, e := wishes.Complete(ctx, f.owner, key, "complete-replay", f.w.ID, version)
		must(e)
		if again.Version != done.Version {
			t.Fatal("重复完成增版本")
		}
		if len(page(f, 0).Items) != 0 {
			t.Fatal("取消消息可读")
		}
		if _, e = wishes.Complete(ctx, f.helper, uuid.NewString(), "foreign", f.w.ID, done.Version); e != command.ErrNotFound {
			t.Fatal("应愿人可完成", e)
		}
		_, e = wishes.Rollback(ctx, f.owner, uuid.NewString(), "restore", f.w.ID, wish.RollbackInput{ExpectedVersion: done.Version, Target: wish.Matching})
		must(e)
		var c entity.Conversation
		must(root.Where("id=?", f.m.ConversationID).Take(&c).Error)
		if c.MatchID != f.m.MatchID || message(a.MessageID).Status != "cancelled" {
			t.Fatal("普通回退改变原关系或复活待审")
		}
	})
	t.Run("确认实现与事务外消息分析并发", func(t *testing.T) {
		f := matched()
		a := send(f, "延迟私聊")
		entered, release := make(chan struct{}), make(chan struct{})
		blocked, e := moderation.NewWorker(workerDB, logger, codec, provider, moderation.WorkerConfig{AnalysisTimeout: 5 * time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}, MessageAnalyzer: privateAnalyzer{entered: entered, release: release}})
		must(e)
		ev := event("message", a.MessageID)
		result := make(chan error, 1)
		go func() { _, e := blocked.Consume(ctx, ev); result <- e }()
		<-entered
		complete(f)
		close(release)
		must(<-result)
		if m := message(a.MessageID); m.Status != "cancelled" || m.DeliverySeq != nil {
			t.Fatal("晚到消息越过确认实现")
		}
		var c entity.Conversation
		must(root.Where("id=?", f.m.ConversationID).Take(&c).Error)
		if c.NextDeliverySeq != 1 {
			t.Fatal("取消消息占用序号")
		}
	})
	t.Run("实际完成释放额度与B01拒绝不阻止还愿", func(t *testing.T) {
		f := matched()
		complete(f)
		for i := 0; i < 3; i++ {
			published(f.owner)
		}
		version := wishRow(f.w.ID).Version
		if _, e = wishes.Rollback(ctx, f.owner, uuid.NewString(), "B01", f.w.ID, wish.RollbackInput{ExpectedVersion: version, Target: wish.Matching}); e != command.ErrActiveWishLimit {
			t.Fatal("B01满额回退未拒绝", e)
		}
		own, e := wishes.Detail(ctx, f.owner, "actions", f.w.ID)
		must(e)
		found := false
		for _, action := range own.AllowedActions {
			found = found || action == "postWishesIdStories"
		}
		if !found {
			t.Fatal("满额错误隐藏还愿动作")
		}
		submit(f, "满额仍可还愿")
		if wishRow(f.w.ID).State != "fulfilled" {
			t.Fatal("还愿被活跃额度阻止")
		}
	})

	t.Run("完整还愿审核改稿分享与撤回", func(t *testing.T) {
		f := matched()
		complete(f)
		a := submit(f, strings.Repeat("鲤", 501))
		if a.Version != wishRow(f.w.ID).Version || a.ContentVersion != 1 {
			t.Fatal("聚合和内容版本错误")
		}
		invisible(f.w.ID)
		scan("story", f.w.ID)
		public, e := wishes.Story(ctx, "", "public", f.w.ID)
		must(e)
		if len([]rune(public.Body)) != 501 || public.SchoolRelated || len(public.NoticeCodes) != 0 {
			t.Fatal("故事字符限制或提示错误")
		}
		link, e := wishes.CreateShare(ctx, f.owner, "story-share-key", "share", wish.ShareInput{TargetKind: "story", ID: f.w.ID})
		must(e)
		replay, e := wishes.CreateShare(ctx, f.owner, "story-share-key", "share", wish.ShareInput{TargetKind: "story", ID: f.w.ID})
		must(e)
		if replay.Ref != link.Ref || strings.Contains(link.MiniappRoute, public.Body) {
			t.Fatal("分享重复或含快照")
		}
		resolved, e := wishes.ResolveShare(ctx, "", "open", link.Ref)
		must(e)
		if resolved.Target.Body != public.Body || resolved.TargetKind != "story" {
			t.Fatal("分享投影不一致")
		}
		update(f, "普通命中 学校命中 新稿")
		invisible(f.w.ID)
		if _, e = wishes.ResolveShare(ctx, "", "hidden", link.Ref); e != command.ErrNotFound {
			t.Fatal("改稿后分享仍返回旧正文", e)
		}
		scan("story", f.w.ID)
		approve(f.w.ID)
		public, e = wishes.Story(ctx, "", "school", f.w.ID)
		must(e)
		resolved, e = wishes.ResolveShare(ctx, "", "school-share", link.Ref)
		must(e)
		if !public.SchoolRelated || public.NoticeText != "未经证实" || len(public.NoticeCodes) != 1 || resolved.Target.ContentVersion != 2 || resolved.Target.NoticeText != public.NoticeText {
			t.Fatal("公开与分享涉校提示不一致")
		}
		out, e := wishes.WithdrawStory(ctx, f.owner, uuid.NewString(), "withdraw", f.w.ID, wishRow(f.w.ID).Version)
		must(e)
		if out.State != wish.Fulfilled {
			t.Fatal("撤回改变已还愿")
		}
		invisible(f.w.ID)
		if _, e = wishes.ResolveShare(ctx, "", "withdrawn", link.Ref); e != command.ErrNotFound {
			t.Fatal("撤回后分享可读", e)
		}
		if _, e = wishes.Rollback(ctx, f.owner, uuid.NewString(), "forbidden", f.w.ID, wish.RollbackInput{ExpectedVersion: out.Version, Target: wish.Matching}); e != command.ErrConflict {
			t.Fatal("已还愿可回退", e)
		}
		update(f, "重新公开新稿")
		scan("story", f.w.ID)
		if _, e = wishes.Story(ctx, "", "fresh", f.w.ID); e != nil {
			t.Fatal("撤回后新稿不能重审", e)
		}
	})
	t.Run("故事驳回状态与建议", func(t *testing.T) {
		f := matched()
		complete(f)
		submit(f, "普通命中")
		scan("story", f.w.ID)
		tr := storyTask(f.w.ID)
		_, e := reviews.Decide(ctx, adminToken, uuid.NewString(), "reject", tr.ID, moderation.Decision{Decision: "rejected", ExpectedVersion: tr.Version, Reason: "合成原因", RevisionSuggestion: "合成建议"})
		must(e)
		own, e := wishes.Detail(ctx, f.owner, "own", f.w.ID)
		must(e)
		if own.State != wish.Fulfilled || own.StoryReviewResult == nil || own.StoryReviewResult.Decision != "rejected" || own.StoryReviewResult.Reason == nil || own.StoryReviewResult.RevisionSuggestion == nil {
			t.Fatal("本人故事审核状态不完整")
		}
		invisible(f.w.ID)
		out := update(f, "合规新稿")
		if out.ContentVersion != 2 {
			t.Fatal("新稿未递增内容版本")
		}
		scan("story", f.w.ID)
		if _, e = wishes.Story(ctx, "", "approved", f.w.ID); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("空故事越权版本与非法状态", func(t *testing.T) {
		f := matched()
		for _, body := range []string{"", " \n\u3000", strings.Repeat("a", 65537)} {
			if _, e = wishes.SubmitStory(ctx, f.owner, uuid.NewString(), "empty", f.w.ID, wish.StoryInput{Body: body, WishExpectedVersion: wishRow(f.w.ID).Version}); e != command.ErrInvalid {
				t.Fatal("无效故事通过", e)
			}
		}
		if _, e = wishes.SubmitStory(ctx, f.owner, uuid.NewString(), "matching", f.w.ID, wish.StoryInput{Body: "故事", WishExpectedVersion: wishRow(f.w.ID).Version}); e != command.ErrConflict {
			t.Fatal("未实现可还愿", e)
		}
		complete(f)
		if _, e = wishes.SubmitStory(ctx, f.helper, uuid.NewString(), "foreign", f.w.ID, wish.StoryInput{Body: "故事", WishExpectedVersion: wishRow(f.w.ID).Version}); e != command.ErrNotFound {
			t.Fatal("非作者还愿", e)
		}
		if _, e = wishes.SubmitStory(ctx, f.owner, uuid.NewString(), "stale", f.w.ID, wish.StoryInput{Body: "故事", WishExpectedVersion: 1}); e != command.ErrVersionConflict {
			t.Fatal("旧版本通过", e)
		}
		submit(f, "正常")
		if _, e = wishes.SubmitStory(ctx, f.owner, uuid.NewString(), "duplicate", f.w.ID, wish.StoryInput{Body: "重复", WishExpectedVersion: wishRow(f.w.ID).Version}); e != command.ErrConflict {
			t.Fatal("重复首次还愿", e)
		}
	})
	t.Run("并发还愿与幂等异请求", func(t *testing.T) {
		f := matched()
		complete(f)
		version := wishRow(f.w.ID).Version
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := wishes.SubmitStory(ctx, f.owner, uuid.NewString(), "concurrent", f.w.ID, wish.StoryInput{Body: "并发故事", WishExpectedVersion: version})
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		success := 0
		for e := range errs {
			if e == nil {
				success++
			} else if e != command.ErrVersionConflict {
				t.Fatal(e)
			}
		}
		if success != 1 || wishRow(f.w.ID).CurrentStoryVersion != 1 {
			t.Fatal("并发重复故事")
		}
		key := uuid.NewString()
		version = wishRow(f.w.ID).Version
		_, e := wishes.UpdateStory(ctx, f.owner, key, "same", f.w.ID, wish.StoryUpdate{Body: "幂等", ExpectedVersion: version})
		must(e)
		if _, e = wishes.UpdateStory(ctx, f.owner, key, "different", f.w.ID, wish.StoryUpdate{Body: "不同", ExpectedVersion: version}); e != command.ErrIdempotencyConflict {
			t.Fatal("同键不同正文通过", e)
		}
	})
	t.Run("故事工作失败整单回滚", func(t *testing.T) {
		f := matched()
		complete(f)
		version := wishRow(f.w.ID).Version
		must(root.Exec("REVOKE INSERT ON public.outbox_events FROM liyuanci_api").Error)
		_, failure := wishes.SubmitStory(ctx, f.owner, uuid.NewString(), "rollback", f.w.ID, wish.StoryInput{Body: "失败故事", WishExpectedVersion: version})
		must(root.Exec("GRANT INSERT ON public.outbox_events TO liyuanci_api").Error)
		if failure == nil || wishRow(f.w.ID).State != "realized" || wishRow(f.w.ID).CurrentStoryVersion != 0 {
			t.Fatal("失败还愿未回滚")
		}
		var n int64
		must(root.Model(&entity.StoryVersion{}).Where("wish_id=?", f.w.ID).Count(&n).Error)
		if n != 0 {
			t.Fatal("失败留下内容版本")
		}
	})
	t.Run("旧审核改稿与撤回后不复活", func(t *testing.T) {
		for _, withdraw := range []bool{false, true} {
			f := matched()
			complete(f)
			submit(f, "待审旧稿")
			old := event("story", f.w.ID)
			if withdraw {
				_, e = wishes.WithdrawStory(ctx, f.owner, uuid.NewString(), "withdraw", f.w.ID, wishRow(f.w.ID).Version)
				must(e)
			} else {
				update(f, "新稿")
			}
			_, e := worker.Consume(ctx, old)
			must(e)
			invisible(f.w.ID)
			if wishRow(f.w.ID).State != "fulfilled" {
				t.Fatal("旧审核回退状态")
			}
		}
	})
	t.Run("心愿分享状态过期与注销重验", func(t *testing.T) {
		for _, kind := range []string{"withdraw", "expired", "deleting", "matching"} {
			t.Run(kind, func(t *testing.T) {
				ownerID, owner := student()
				w := published(owner)
				key := uuid.NewString()
				link, e := wishes.CreateShare(ctx, owner, key, "wish-share", wish.ShareInput{TargetKind: "wish", ID: w.ID})
				must(e)
				if _, e = wishes.ResolveShare(ctx, "", "before", link.Ref); e != nil {
					t.Fatal(e)
				}
				switch kind {
				case "withdraw":
					_, e = wishes.Withdraw(ctx, owner, uuid.NewString(), "withdraw", w.ID, wishRow(w.ID).Version)
					must(e)
				case "expired":
					must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Update("expires_at", time.Now().Add(-time.Second)).Error)
				case "deleting":
					must(root.Model(&entity.Account{}).Where("id=?", ownerID).Update("lifecycle", "deleting").Error)
				default:
					_, helper := student()
					a := apply(helper, w, "应愿")
					scan("application", a.ID)
					accept(owner, w, a.ID)
				}
				if _, e = wishes.ResolveShare(ctx, "", "after", link.Ref); e != command.ErrNotFound {
					t.Fatal("失效分享返回旧正文", e)
				}
			})
		}
	})
	// 仅模拟未来注销生效事务的屏障/代次，不冒称完整注销及物理清理服务。
	deletion := func(f fixture, epoch, required int64) {
		t.Helper()
		must(root.Transaction(func(db *gorm.DB) error {
			p, e := wish.DiscoverStory(ctx, db, f.w.ID)
			if e != nil {
				return e
			}
			_, e = command.LockPlan(ctx, db, p, time.Second)
			if e != nil {
				return e
			}
			if e = db.Model(&entity.Account{}).Where("id=?", f.helperID).Updates(map[string]any{"lifecycle": "deleting", "auth_epoch": gorm.Expr("auth_epoch+1")}).Error; e != nil {
				return e
			}
			return db.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "story", TargetID: f.w.ID, HoldEpoch: epoch, RequiredContentVersion: required}).Error
		}))
	}
	t.Run("隐私屏障暂停清理隐藏新脱敏版本才恢复", func(t *testing.T) {
		f := matched()
		complete(f)
		submit(f, "含合成个人姓名旧稿")
		scan("story", f.w.ID)
		link, e := wishes.CreateShare(ctx, f.owner, uuid.NewString(), "share", wish.ShareInput{TargetKind: "story", ID: f.w.ID})
		must(e)
		deletion(f, 1, 2)
		invisible(f.w.ID)
		if _, e = wishes.ResolveShare(ctx, f.owner, "owner-share", link.Ref); e != command.ErrNotFound {
			t.Fatal("作者通过分享取旧正文", e)
		}
		list, e := wishes.Stories(ctx, "hidden-list", "", 50)
		must(e)
		for _, item := range list.Items {
			if item.ID == f.w.ID {
				t.Fatal("屏障故事仍在列表")
			}
		}
		own, e := wishes.Detail(ctx, f.owner, "own-held", f.w.ID)
		must(e)
		if !own.PrivacyReviewRequired || own.OwnContent != nil {
			t.Fatal("本人屏障视图包含旧内容")
		}
		out := update(f, "去除身份后的新稿")
		if out.ContentVersion != 2 {
			t.Fatal("重审未新建版本")
		}
		scan("story", f.w.ID)
		v := storyVersion(f.w.ID)
		if v.SourceMatchID != nil || v.PeerEpoch != nil {
			t.Fatal("脱敏重审保留已注销对方关联")
		}
		// 注销隐私新稿必须人工确认，不能把扫描完成当作重新公开。
		tr := storyTask(f.w.ID)
		if tr.Status != "human_required" {
			t.Fatal("隐私新稿未强制人工复核")
		}
		_, e = reviews.Decide(ctx, adminToken, uuid.NewString(), "privacy-review", tr.ID, moderation.Decision{Decision: "approved", ExpectedVersion: tr.Version})
		must(e)
		if _, e = wishes.ResolveShare(ctx, "", "new-public", link.Ref); e != nil {
			t.Fatal("新脱敏审核不能恢复分享", e)
		}
		var hold entity.PrivacyHold
		must(root.Where("target_kind='story' AND target_id=?", f.w.ID).Take(&hold).Error)
		if hold.Status != "released" || hold.ReleasedAt == nil {
			t.Fatal("对应屏障未原子释放")
		}
	})
	t.Run("新屏障打断事务外分析与旧审不能释放", func(t *testing.T) {
		f := matched()
		complete(f)
		submit(f, "普通命中 旧待审")
		ev := event("story", f.w.ID)
		entered, release := make(chan struct{}), make(chan struct{})
		blocking := &analyzer{}
		blocking.set(false, func() { close(entered); <-release })
		other, e := moderation.NewWorker(workerDB, logger, codec, blocking, moderation.WorkerConfig{AnalysisTimeout: 5 * time.Second, Consumer: durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}})
		must(e)
		result := make(chan error, 1)
		go func() { _, e := other.Consume(ctx, ev); result <- e }()
		<-entered
		deletion(f, 1, 2)
		close(release)
		must(<-result)
		invisible(f.w.ID)
		var hold entity.PrivacyHold
		must(root.Where("target_kind='story' AND target_id=?", f.w.ID).Take(&hold).Error)
		if hold.Status != "active" {
			t.Fatal("晚到审核释放屏障")
		}
	})
	t.Run("危机隔离与父屏障不被故事释放", func(t *testing.T) {
		f := matched()
		complete(f)
		submit(f, "危机命中 普通命中")
		scan("story", f.w.ID)
		invisible(f.w.ID)
		if _, e = reviews.Case(ctx, adminToken, "crisis", storyTask(f.w.ID).ID); e != command.ErrNotFound {
			t.Fatal("普通运营可读危机故事", e)
		}
		f = matched()
		complete(f)
		submit(f, "待审")
		must(root.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "wish", TargetID: f.w.ID, HoldEpoch: 1, RequiredContentVersion: 2}).Error)
		cancelled, e := worker.Consume(ctx, event("story", f.w.ID))
		must(e)
		if !cancelled.Cancelled || cancelled.Applied {
			t.Fatal("父屏障变化未取消旧审核")
		}

		invisible(f.w.ID)
		var hold entity.PrivacyHold
		must(root.Where("target_kind='wish' AND target_id=?", f.w.ID).Take(&hold).Error)
		if hold.Status != "active" {
			t.Fatal("故事审核错误释放父内容屏障")
		}
	})
	t.Run("公开故事分页与游标用途隔离", func(t *testing.T) {
		var latest string
		for i := 0; i < 2; i++ {
			f := matched()
			complete(f)
			submit(f, "分页公开故事")
			scan("story", f.w.ID)
			latest = f.w.ID
		}
		first, e := wishes.Stories(ctx, "page", "", 1)
		must(e)
		if len(first.Items) != 1 || first.NextCursor == nil || first.Items[0].ID != latest {
			t.Fatal("缺少故事分页位置")
		}
		next, e := wishes.Stories(ctx, "next", *first.NextCursor, 1)
		must(e)
		if len(next.Items) != 1 || next.Items[0].ID == first.Items[0].ID {
			t.Fatal("故事分页重复")
		}
		if _, e = wishes.List(ctx, "wrong-scope", wish.Query{Cursor: *first.NextCursor, Limit: 1}); e != command.ErrInvalid {
			t.Fatal("故事游标可跨广场复用", e)
		}
		if _, e = wishes.Stories(ctx, "tampered", *first.NextCursor+"x", 1); e != command.ErrInvalid {
			t.Fatal("签名游标未校验", e)
		}
	})

	t.Run("真实HTTP八接口严格输入", func(t *testing.T) {
		f := matched()
		r := httpapi.New(logger, nil)
		must(httpapi.RegisterWishes(r, wishes))
		must(httpapi.RegisterStories(r, wishes))
		request := func(method, path, token, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", uuid.NewString())
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			return out
		}
		base := "/api/v1/wishes/" + f.w.ID
		body, _ := json.Marshal(map[string]any{"expected_version": wishRow(f.w.ID).Version})
		if out := request("POST", base+"/complete", f.owner, string(body)); out.Code != 200 {
			t.Fatal("完成HTTP失败", out.Code)
		}
		for _, bad := range []string{`{"body":"x"}`, `{"body":null,"wish_expected_version":1}`, `{"body":"x","body":"y","wish_expected_version":1}`, `{"Body":"x","wish_expected_version":1}`, `{"body":"x","wish_expected_version":1,"state":"fulfilled"}`} {
			if out := request("POST", base+"/stories", f.owner, bad); out.Code != 400 {
				t.Fatal("非法故事字段未拒绝", out.Code)
			}
		}
		body, _ = json.Marshal(wish.StoryInput{Body: "HTTP故事", WishExpectedVersion: wishRow(f.w.ID).Version})
		if out := request("POST", base+"/stories", f.owner, string(body)); out.Code != 202 || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("还愿HTTP失败", out.Code)
		}
		scan("story", f.w.ID)
		for _, path := range []string{"/api/v1/stories", "/api/v1/stories/" + f.w.ID} {
			if out := request("GET", path, "", ""); out.Code != 200 {
				t.Fatal("公开HTTP失败", out.Code)
			}
		}
		out := request("POST", "/api/v1/share-links", f.owner, `{"target_kind":"story","id":"`+f.w.ID+`"}`)
		if out.Code != 201 {
			t.Fatal("分享HTTP失败", out.Code, out.Body.String())
		}
		var payload struct {
			Data wish.ShareLink `json:"data"`
		}
		must(json.Unmarshal(out.Body.Bytes(), &payload))
		if out = request("GET", "/api/v1/share-links/"+payload.Data.Ref, "", ""); out.Code != 200 {
			t.Fatal("解析HTTP失败", out.Code)
		}
		body, _ = json.Marshal(wish.StoryUpdate{Body: "HTTP新稿", ExpectedVersion: wishRow(f.w.ID).Version})
		if out = request("PUT", base+"/story", f.owner, string(body)); out.Code != 202 {
			t.Fatal("改稿HTTP失败", out.Code)
		}
		body, _ = json.Marshal(map[string]any{"expected_version": wishRow(f.w.ID).Version})
		if out = request("POST", base+"/story/withdraw", f.owner, string(body)); out.Code != 200 {
			t.Fatal("撤回HTTP失败", out.Code)
		}
		if out = request("GET", "/api/v1/stories?limit=51", "", ""); out.Code != 400 {
			t.Fatal("技术分页上限失效")
		}
	})
	if nameserver := os.Getenv("APP_MODERATION_TEST_NAMESERVER"); nameserver != "" {
		t.Run("真实MQ故事重投不重复审核版本", func(t *testing.T) {
			f := matched()
			complete(f)
			submit(f, "MQ故事")
			ev := event("story", f.w.ID)
			before := wishRow(f.w.ID).Version
			unique := "story_" + uuid.NewString()[:8]
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
					t.Fatal("故事MQ重投超时")
				}
			}
			if wishRow(f.w.ID).Version != before+1 {
				t.Fatal("重复扫描重复增版本")
			}
			if _, e = wishes.Story(ctx, "", "MQ公开", f.w.ID); e != nil {
				t.Fatal(e)
			}
		})
	}
	var missing int64
	must(root.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname IN ('public','evidence') AND c.relkind='r' AND (coalesce(obj_description(c.oid,'pg_class'),'')='' OR coalesce(col_description(c.oid,a.attnum),'')='')`).Scan(&missing).Error)
	if missing != 0 {
		t.Fatal("数据库对象注释缺失")
	}
	if !t.Failed() {
		t.Log("T023真实PG/HTTP/MQ完成、还愿与分享通过；身份、密钥、分析器及注销入口为合成")
	}
}
