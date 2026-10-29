package moderation_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
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
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// TestSafetyGovernance 验证T026、REQ-004/005/006/007/014/016/019的真实数据库和HTTP边界。
// 身份、规则与分析供应商均为合成；警告时间夹具调整服务器持久时刻，不等待真实三十秒。
func TestSafetyGovernance(t *testing.T) {
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
	database := "safety_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	students, e := safety.New(api, logger, codec, safety.Config{Command: cfg, Key: []byte(strings.Repeat("s", 32)), MaxTextBytes: 65536})
	must(e)
	admins, e := safety.New(adminDB, logger, codec, safety.Config{Command: cfg, Key: []byte(strings.Repeat("s", 32)), MaxTextBytes: 65536})
	must(e)
	router := httpapi.New(logger, func(context.Context) error { return nil })
	must(httpapi.RegisterSafety(router, students, admins))
	call := func(token, method, path, key, body string, status int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, rr.Code, status, rr.Body.String())
		}
		if status == 204 {
			return nil
		}
		var envelope map[string]any
		must(json.Unmarshal(rr.Body.Bytes(), &envelope))
		if status >= 400 {
			return envelope
		}
		return envelope["data"].(map[string]any)
	}
	exec := func(service *safety.Service, token, op, id string, in safety.Input) any {
		t.Helper()
		out, e := service.Execute(ctx, token, uuid.NewString(), "safety", op, id, in)
		must(e)
		return out
	}
	requireError := func(e, want error) {
		t.Helper()
		if !errors.Is(e, want) {
			t.Fatalf("got %v want %v", e, want)
		}
	}
	must(root.Create(&entity.ReportType{Code: "synthetic", Label: "合成举报类型", Published: true}).Error)
	confirmed := func(reporter string, w wish.CreatedDraft) string {
		t.Helper()
		r := exec(students, reporter, "report", "", safety.Input{ID: w.ID, TargetKind: "wish", TypeCode: "synthetic", Description: "合成案件说明"}).(safety.Summary)
		detail, e := admins.Detail(ctx, adminToken, "report", "report_detail", r.ID)
		must(e)
		if detail.(map[string]any)["body"] != "合成招募心愿" {
			t.Fatalf("案件未提供限定正文: %v", detail)
		}
		exec(admins, adminToken, "report_decision", r.ID, safety.Input{Decision: "confirmed", Reason: "合成已核实", ExpectedVersion: r.Version})
		return r.ID
	}
	impose := func(ownerID, reportID, wid, level string) safety.Summary {
		t.Helper()
		var c entity.ContentRegistry
		must(root.Where("kind='wish' AND entity_id=?", wid).Take(&c).Error)
		var a entity.Account
		must(root.Where("id=?", ownerID).Take(&a).Error)
		// 用户详情提供真实Account.version，普通处置继续在原案件核验作者与内容版本。
		detail := call(adminToken, "GET", "/admin/users/"+ownerID, "", "", 200)
		version := int64(detail["account_version"].(float64))
		if version != a.Version {
			t.Fatal("用户详情版本与账号权威值不一致")
		}
		return exec(admins, adminToken, "penalty", ownerID, safety.Input{ViolationRef: reportID, ContentRef: c.ID, ContentVersion: c.ContentVersion, Level: level, Reason: "合成处置理由", ExpectedVersion: version}).(safety.Summary)
	}
	t.Run("拉黑双向旧申请永久失效及幂等", func(t *testing.T) {
		oi, ot := student()
		hi, ht := student()
		w := published(ot)
		a := apply(ht, w, "合成留言")
		scan("application", a.ID)
		key := uuid.NewString()
		body := fmt.Sprintf(`{"target_user_id":%q}`, oi)
		call(ht, "POST", "/me/blocks", key, body, 200)
		call(ht, "POST", "/me/blocks", key, body, 200)
		if appRow(a.ID).Status != "invalidated" {
			t.Fatal("旧申请未失效")
		}
		_, e := wishes.Apply(ctx, ht, uuid.NewString(), "blocked", w.ID, wish.ApplicationInput{Message: "重申", WishContentVersion: 1})
		requireError(e, command.ErrBlockedInteraction)
		call(ht, "DELETE", "/me/blocks/"+oi, uuid.NewString(), `{"expected_version":99}`, 409)
		call(ht, "DELETE", "/me/blocks/"+oi, uuid.NewString(), `{"expected_version":2}`, 204)
		if appRow(a.ID).Status != "invalidated" {
			t.Fatal("解除拉黑复活旧申请")
		}
		exec(students, ot, "block", "", safety.Input{TargetUserID: hi})
		exec(students, ht, "block", "", safety.Input{TargetUserID: oi})
		call(ht, "DELETE", "/me/blocks/"+oi, uuid.NewString(), `{"expected_version":2}`, 409)
		exec(students, ht, "unblock", oi, safety.Input{ExpectedVersion: 3})
		_, e = wishes.Apply(ctx, ht, uuid.NewString(), "blocked", w.ID, wish.ApplicationInput{Message: "重申", WishContentVersion: 1})
		requireError(e, command.ErrBlockedInteraction)
		exec(students, ot, "unblock", hi, safety.Input{ExpectedVersion: 2})
		apply(ht, w, "重新主动申请")
	})
	t.Run("本人黑名单分页版本与游标隔离", func(t *testing.T) {
		owner, token := student()
		other, otherToken := student()
		empty := call(token, "GET", "/me/blocks", "", "", 200)
		if len(empty["items"].([]any)) != 0 || empty["next_cursor"] != nil {
			t.Fatal("空列表边界错误", empty)
		}
		for i := 0; i < 3; i++ {
			target, _ := student()
			call(token, "POST", "/me/blocks", uuid.NewString(), fmt.Sprintf(`{"target_user_id":%q}`, target), 200)
		}
		must(root.Exec("UPDATE public.blocks SET created_at=? WHERE owner_id=?", time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond), owner).Error)
		first := call(token, "GET", "/me/blocks?limit=1", "", "", 200)
		cursor, ok := first["next_cursor"].(string)
		if !ok || cursor == "" || len(first["items"].([]any)) != 1 {
			t.Fatal("缺少分页游标", first)
		}
		entry := first["items"].([]any)[0].(map[string]any)
		if len(entry) != 2 || entry["version"].(float64) < 1 {
			t.Fatal("条目投影扩大或缺少版本", entry)
		}
		seen := map[string]bool{}
		previous, next := "", ""
		for {
			path := "/me/blocks?limit=1"
			if next != "" {
				path += "&cursor=" + url.QueryEscape(next)
			}
			page := call(token, "GET", path, "", "", 200)
			items := page["items"].([]any)
			if len(items) != 1 {
				t.Fatal("分页缺项", page)
			}
			id := items[0].(map[string]any)["target_user_id"].(string)
			if seen[id] || previous != "" && id >= previous {
				t.Fatal("游标重复或同时间排序错误", id)
			}
			seen[id], previous = true, id
			if page["next_cursor"] == nil {
				break
			}
			next = page["next_cursor"].(string)
		}
		if len(seen) != 3 {
			t.Fatal("黑名单分页不完整", seen)
		}
		for _, request := range []struct{ token, path string }{
			{otherToken, "/me/blocks?cursor=" + url.QueryEscape(cursor)},
			{token, "/me/warnings?cursor=" + url.QueryEscape(cursor)},
			{token, "/me/blocks?cursor=" + url.QueryEscape(cursor+"x")},
		} {
			call(request.token, "GET", request.path, "", "", 400)
		}
		call(token, "DELETE", "/me/blocks/"+entry["target_user_id"].(string), uuid.NewString(), fmt.Sprintf(`{"expected_version":%.0f}`, entry["version"]), 204)
		// 游标指向的条目被解除也不会重回首页；仍可从原位置向后读取。
		afterDelete := call(token, "GET", "/me/blocks?limit=1&cursor="+url.QueryEscape(cursor), "", "", 200)
		if len(afterDelete["items"].([]any)) != 1 || afterDelete["items"].([]any)[0].(map[string]any)["target_user_id"] == entry["target_user_id"] {
			t.Fatal("历史位置游标恢复旧条目", afterDelete)
		}
		ownOther := call(otherToken, "GET", "/me/blocks", "", "", 200)
		if len(ownOther["items"].([]any)) != 0 {
			t.Fatal("跨账号读取黑名单", other, ownOther)
		}
		must(root.Exec("UPDATE public.sessions SET revoked_at=clock_timestamp() WHERE account_id=?", owner).Error)
		call(token, "GET", "/me/blocks", "", "", 401)
	})
	t.Run("警告首次展示及提前同键重试", func(t *testing.T) {
		oi, ot := student()
		_, rt := student()
		w := published(ot)
		v := confirmed(rt, w)
		p := impose(oi, v, w.ID, "minor")
		var row entity.Penalty
		must(root.Where("id=?", p.ID).Take(&row).Error)
		if row.ActionKind != "warning" || row.EndsAt != nil {
			t.Fatal("警告错误限制")
		}
		call(rt, "POST", "/me/warnings/"+p.ID+"/display", uuid.NewString(), `{"expected_version":1}`, 404)
		key := uuid.NewString()
		path := "/me/warnings/" + p.ID + "/ack"
		call(ot, "POST", path, key, `{"expected_version":1}`, 409)
		shown := call(ot, "POST", "/me/warnings/"+p.ID+"/display", uuid.NewString(), `{"expected_version":1}`, 200)
		first := shown["first_displayed_at"]
		again := call(ot, "POST", "/me/warnings/"+p.ID+"/display", uuid.NewString(), `{"expected_version":2}`, 200)
		if first != again["first_displayed_at"] {
			t.Fatal("重进重置服务器起点")
		}
		must(root.Exec("UPDATE public.penalties SET first_displayed_at=statement_timestamp()-interval '29 seconds',confirmable_at=statement_timestamp()+interval '1 second' WHERE id=?", p.ID).Error)
		key = uuid.NewString()
		call(ot, "POST", path, key, `{"expected_version":2}`, 409)
		must(root.Exec("UPDATE public.penalties SET first_displayed_at=statement_timestamp()-interval '31 seconds',confirmable_at=statement_timestamp()-interval '1 second' WHERE id=?", p.ID).Error)
		call(ot, "POST", path, key, `{"expected_version":2}`, 200)
		call(ot, "POST", path, key, `{"expected_version":2}`, 200)
		// 轻微警告不剥夺发布权限，违规内容的限制独立保留。
		published(ot)
		var n int64
		must(root.Model(&entity.Notification{}).Where("recipient_id=? AND type='penalty_notice'", oi).Count(&n).Error)
		if n != 1 {
			t.Fatal("重复处罚通知")
		}
		p2 := impose(oi, v, w.ID, "minor")
		if p.ID != p2.ID {
			t.Fatal("同违规跨键重复警告")
		}
	})
	t.Run("禁言暂停待审取消申诉解罚不复活", func(t *testing.T) {
		oi, ot := student()
		_, ht := student()
		_, rt := student()
		w := published(ot)
		app := apply(ht, w, "匹配留言")
		scan("application", app.ID)
		m := accept(ot, w, app.ID)
		var conversation entity.Conversation
		must(root.Where("id=?", m.ConversationID).Take(&conversation).Error)
		msg, e := chats.Send(ctx, ht, uuid.NewString(), "message", m.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "待审合成消息", ExpectedWriteEpoch: conversation.WriteEpoch})
		must(e)
		reportWish := published(ot)
		violation := confirmed(rt, reportWish)
		p := impose(oi, violation, reportWish.ID, "general")
		var row entity.Penalty
		must(root.Where("id=?", p.ID).Take(&row).Error)
		if row.ActionKind != "mute" || row.EndsAt.Sub(row.StartsAt) != 168*time.Hour {
			t.Fatal("禁言不是固定七天")
		}
		if wishRow(w.ID).State != "matching" {
			t.Fatal("处罚自动解绑")
		}
		var ms entity.Message
		must(root.Where("id=?", msg.MessageID).Take(&ms).Error)
		if ms.Status != "cancelled" {
			t.Fatal("待审消息未取消")
		}
		_, e = chats.Send(ctx, ht, uuid.NewString(), "message", m.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "受罚期间", ExpectedWriteEpoch: conversation.WriteEpoch})
		requireError(e, command.ErrForbidden)
		a := exec(students, ot, "appeal", "", safety.Input{PenaltyID: p.ID, Body: "合成申诉正文"}).(safety.Appeal)
		detail, e := admins.Detail(ctx, adminToken, "appeal", "appeal_detail", a.ID)
		must(e)
		if detail.(map[string]any)["body"] != "合成申诉正文" {
			t.Fatal("申诉正文不可审阅")
		}
		exec(admins, adminToken, "appeal_decision", a.ID, safety.Input{Decision: "revoke", Reason: "合成解罚理由", ExpectedVersion: 1})
		must(root.Where("id=?", msg.MessageID).Take(&ms).Error)
		if ms.Status != "cancelled" {
			t.Fatal("解罚复活旧消息")
		}
		if wishRow(reportWish.ID).State != "withdrawn" {
			t.Fatal("解罚自动上架")
		}
		var view chat.ConversationsPage
		view, e = chats.Conversations(ctx, ht, "after-revoke", chat.Query{Limit: 50})
		must(e)
		if len(view.Items) != 1 || !view.Items[0].CanWrite {
			t.Fatalf("保留匹配未恢复:%+v", view)
		}
	})
	t.Run("用户查询随申诉仅解除对应处罚", func(t *testing.T) {
		oi, ot := student()
		_, rt := student()
		first, second := published(ot), published(ot)
		firstReport, secondReport := confirmed(rt, first), confirmed(rt, second)
		p := impose(oi, firstReport, first.ID, "general")
		impose(oi, secondReport, second.ID, "general")
		detail := call(adminToken, "GET", "/admin/users/"+oi, "", "", 200)
		if detail["restriction"] != "muted" || len(detail["active_penalties"].([]any)) != 2 {
			t.Fatal("多项禁言投影不完整")
		}
		a := exec(students, ot, "appeal", "", safety.Input{PenaltyID: p.ID, Body: "合成单项申诉"}).(safety.Appeal)
		appealDetail, e := admins.Detail(ctx, adminToken, "appeal-version", "appeal_detail", a.ID)
		must(e)
		version := appealDetail.(map[string]any)["version"].(int64)
		exec(admins, adminToken, "appeal_decision", a.ID, safety.Input{Decision: "revoke", Reason: "只撤对应处罚", ExpectedVersion: version})
		detail = call(adminToken, "GET", "/admin/users/"+oi, "", "", 200)
		if detail["restriction"] != "muted" || len(detail["active_penalties"].([]any)) != 1 {
			t.Fatal("撤销一项错误解除全部禁言")
		}
	})
	t.Run("永久封禁仍可申诉且无普通写入", func(t *testing.T) {
		oi, ot := student()
		ri, rt := student()
		w := published(ot)
		p := impose(oi, confirmed(rt, w), w.ID, "severe")
		var row entity.Penalty
		must(root.Where("id=?", p.ID).Take(&row).Error)
		if row.ActionKind != "ban" || row.EndsAt != nil {
			t.Fatal("封禁存在自动期限")
		}
		_, e = chats.Conversations(ctx, ot, "ban", chat.Query{Limit: 20})
		requireError(e, command.ErrForbidden)
		notices, e := chats.Notifications(ctx, ot, "penalty", chat.Query{Limit: 20})
		must(e)
		if len(notices.Items) != 1 || notices.Items[0].TargetRef == nil || *notices.Items[0].TargetRef != p.ID {
			t.Fatal("封禁未提供申诉目标引用")
		}
		call(ot, "GET", "/me/blocks", "", "", 403)
		call(ot, "POST", "/me/blocks", uuid.NewString(), fmt.Sprintf(`{"target_user_id":%q}`, ri), 403)
		a := exec(students, ot, "appeal", "", safety.Input{PenaltyID: p.ID, Body: "封禁本人申诉"}).(safety.Appeal)
		_, e = students.List(ctx, ot, "appeals", "appeals", safety.Query{Limit: 20})
		must(e)
		_, e = students.Execute(ctx, rt, uuid.NewString(), "appeal", "appeal", "", safety.Input{PenaltyID: p.ID, Body: "他人申诉"})
		requireError(e, command.ErrNotFound)
		call(ot, "POST", "/wishes/"+w.ID+"/exception-exit", uuid.NewString(), `{"expected_version":1}`, 403)
		exec(admins, adminToken, "appeal_decision", a.ID, safety.Input{Decision: "uphold", Reason: "维持合成处置", ExpectedVersion: 1})
	})
	t.Run("拉黑例外退出及已还愿拒绝", func(t *testing.T) {
		oi, ot := student()
		_, ht := student()
		w := published(ot)
		app := apply(ht, w, "合成留言")
		scan("application", app.ID)
		m := accept(ot, w, app.ID)
		exec(students, ht, "block", "", safety.Input{TargetUserID: oi})
		_, e := students.Execute(ctx, ot, uuid.NewString(), "exit", "exception_exit", w.ID, safety.Input{ExpectedVersion: wishRow(w.ID).Version})
		requireError(e, command.ErrForbidden)
		before := wishRow(w.ID)
		out := call(ht, "POST", "/wishes/"+w.ID+"/exception-exit", uuid.NewString(), fmt.Sprintf(`{"expected_version":%d}`, before.Version), 200)
		if out["current_state"] != "withdrawn" || out["content_version"] == nil || out["allowed_actions"] == nil {
			t.Fatal("例外退出响应偏离心愿契约")
		}
		after := wishRow(w.ID)
		if after.State != "withdrawn" || !after.ExpiresAt.Equal(*before.ExpiresAt) || after.RefreshOnPublish {
			t.Fatal("例外退出目标或期限错误")
		}
		var match entity.Match
		must(root.Where("id=?", m.MatchID).Take(&match).Error)
		if match.Status != "ended" {
			t.Fatal("匹配未解除")
		}
		_, e = chats.Send(ctx, ht, uuid.NewString(), "after-exit", m.ConversationID, chat.MessageInput{ClientMessageID: uuid.NewString(), Body: "旧会话", ExpectedWriteEpoch: 1})
		requireError(e, command.ErrForbidden)
	})
	t.Run("举报鉴权类型版本及角色重验", func(t *testing.T) {
		oi, ot := student()
		_, rt := student()
		w := published(ot)
		call(rt, "POST", "/reports", uuid.NewString(), fmt.Sprintf(`{"target_kind":"wish","id":%q,"type_code":"missing"}`, w.ID), 400)
		r := exec(students, rt, "report", "", safety.Input{ID: w.ID, TargetKind: "wish", TypeCode: "synthetic"}).(safety.Summary)
		call(ot, "GET", "/admin/reports", "", "", 401)
		call(adminToken, "POST", "/admin/reports/"+r.ID+"/decision", uuid.NewString(), `{"decision":"confirmed","reason":"合成","expected_version":2}`, 409)
		exec(admins, adminToken, "report_decision", r.ID, safety.Input{Decision: "confirmed", Reason: "合成", ExpectedVersion: 1})
		var c entity.ContentRegistry
		must(root.Where("kind='wish' AND entity_id=?", w.ID).Take(&c).Error)
		wrong, _ := student()
		detail := call(adminToken, "GET", "/admin/users/"+oi, "", "", 200)
		call(adminToken, "POST", "/admin/users/"+oi+"/penalties", uuid.NewString(), fmt.Sprintf(`{"violation_ref":%q,"content_ref":%q,"content_version":1,"level":"general","reason":"版本冲突合成","expected_version":%.0f}`, r.ID, c.ID, detail["account_version"].(float64)+1), 409)
		_, e := admins.Execute(ctx, adminToken, uuid.NewString(), "penalty", "penalty", wrong, safety.Input{ViolationRef: r.ID, ContentRef: c.ID, ContentVersion: 1, Level: "minor", Reason: "错主体", ExpectedVersion: 1})
		requireError(e, command.ErrNotFound)
		call(adminToken, "POST", "/admin/users/"+oi+"/penalties", uuid.NewString(), fmt.Sprintf(`{"violation_ref":%q,"content_ref":%q,"content_version":1,"level":"general","reason":"合成","expected_version":1,"duration_days":1}`, r.ID, c.ID), 400)
		must(root.Where("admin_id=?", admin).Delete(&entity.AdminRoleBinding{}).Error)
		call(adminToken, "GET", "/admin/reports", "", "", 403)
		must(root.Create(&entity.AdminRoleBinding{AdminID: admin, Role: "review_operator"}).Error)
	})
	t.Run("内容普通限制解除不自动公开", func(t *testing.T) {
		_, ot := student()
		w := published(ot)
		exec(admins, adminToken, "offline", w.ID, safety.Input{TargetKind: "wish", Reason: "合成下线", ExpectedVersion: w.Version})
		row := wishRow(w.ID)
		if row.State != "withdrawn" {
			t.Fatal("下线未撤下")
		}
		out := exec(admins, adminToken, "release", w.ID, safety.Input{TargetKind: "wish", Reason: "合成解除", ExpectedVersion: row.Version}).(safety.Summary)
		if out.Status != "released" || wishRow(w.ID).State != "withdrawn" {
			t.Fatal("解除自动上架")
		}
	})
	t.Run("例外退出跨状态处罚主体满额与过期矩阵", func(t *testing.T) {
		for _, state := range []string{"matching", "realized"} {
			for _, cause := range []string{"block", "penalty"} {
				for _, side := range []string{"owner", "helper"} {
					t.Run(state+"/"+cause+"/"+side, func(t *testing.T) {
						oi, ot := student()
						hi, ht := student()
						_, rt := student()
						w := published(ot)
						a := apply(ht, w, "矩阵留言")
						scan("application", a.ID)
						accept(ot, w, a.ID)
						if state == "realized" {
							_, e := wishes.Complete(ctx, ot, uuid.NewString(), "complete", w.ID, wishRow(w.ID).Version)
							must(e)
						}
						// 真实发布填满作者剩余额度，已实现不占额度。例外退出不经过普通回退限额。
						count := 2
						if state == "realized" {
							count = 3
						}
						var ownerSource wish.CreatedDraft
						for j := 0; j < count; j++ {
							ownerSource = published(ot)
						}
						actor, peer := ot, hi
						if side == "helper" {
							actor, peer = ht, oi
						}
						if cause == "block" {
							exec(students, actor, "block", "", safety.Input{TargetUserID: peer})
						} else {
							targetToken := ht
							if side == "helper" {
								targetToken = ot
							}
							pw := ownerSource
							if side == "owner" {
								pw = published(targetToken)
							}
							// 作者已满额时发布返回ready；单独案件夹具保持正式可见内容用于处罚来源。
							if pw.State != wish.Published {
								t.Fatal("处罚来源未上架")
							}
							impose(peer, confirmed(rt, pw), pw.ID, "general")
						}
						must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Update("expires_at", time.Now().Add(-time.Hour)).Error)
						before := wishRow(w.ID)
						exec(students, actor, "exception_exit", w.ID, safety.Input{ExpectedVersion: before.Version})
						after := wishRow(w.ID)
						if after.State != "withdrawn" || !after.ExpiresAt.Equal(*before.ExpiresAt) {
							t.Fatal("例外退出违反状态或期限")
						}
					})
				}
			}
		}
	})
	t.Run("已还愿拒绝例外退出", func(t *testing.T) {
		oi, ot := student()
		_, ht := student()
		w := published(ot)
		a := apply(ht, w, "还愿留言")
		scan("application", a.ID)
		accept(ot, w, a.ID)
		complete, e := wishes.Complete(ctx, ot, uuid.NewString(), "complete", w.ID, wishRow(w.ID).Version)
		must(e)
		_, e = wishes.SubmitStory(ctx, ot, uuid.NewString(), "story", w.ID, wish.StoryInput{Body: "合成还愿故事", WishExpectedVersion: complete.Version})
		must(e)
		exec(students, ht, "block", "", safety.Input{TargetUserID: oi})
		_, e = students.Execute(ctx, ht, uuid.NewString(), "exit", "exception_exit", w.ID, safety.Input{ExpectedVersion: wishRow(w.ID).Version})
		requireError(e, command.ErrConflict)
	})
	t.Run("处罚通知失败整单回滚", func(t *testing.T) {
		oi, ot := student()
		_, rt := student()
		w := published(ot)
		r := confirmed(rt, w)
		must(root.Exec(`CREATE FUNCTION public.reject_safety_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='penalty_notice' THEN RAISE EXCEPTION 'synthetic-notification-failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_safety_notice BEFORE INSERT ON public.notifications FOR EACH ROW EXECUTE FUNCTION public.reject_safety_notice()`).Error)
		var c entity.ContentRegistry
		must(root.Where("kind='wish' AND entity_id=?", w.ID).Take(&c).Error)
		_, e := admins.Execute(ctx, adminToken, uuid.NewString(), "rollback", "penalty", oi, safety.Input{ViolationRef: r, ContentRef: c.ID, ContentVersion: 1, Level: "general", Reason: "回滚合成", ExpectedVersion: 1})
		requireError(e, command.ErrUnavailable)
		must(root.Exec("DROP TRIGGER reject_safety_notice ON public.notifications; DROP FUNCTION public.reject_safety_notice()").Error)
		var n int64
		must(root.Model(&entity.Penalty{}).Where("account_id=?", oi).Count(&n).Error)
		if n != 0 || wishRow(w.ID).State != "published" {
			t.Fatal("失败处罚产生部分生效")
		}
	})
	t.Run("处罚与同意并发原子暂停", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			oi, ot := student()
			_, ht := student()
			_, rt := student()
			w := published(ot)
			a := apply(ht, w, "处罚并发留言")
			scan("application", a.ID)
			ar := appRow(a.ID)
			wr := wishRow(w.ID)
			r := confirmed(rt, w)
			var c entity.ContentRegistry
			must(root.Where("kind='wish' AND entity_id=?", w.ID).Take(&c).Error)
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, e := admins.Execute(ctx, adminToken, uuid.NewString(), "penalty-race", "penalty", oi, safety.Input{ViolationRef: r, ContentRef: c.ID, ContentVersion: 1, Level: "general", Reason: "并发处置", ExpectedVersion: 1})
				errs <- e
			}()
			go func() {
				defer wg.Done()
				_, e := wishes.AcceptApplication(ctx, ot, uuid.NewString(), "accept-race", a.ID, wish.AcceptInput{ExpectedVersion: ar.Version, WishExpectedVersion: wr.Version})
				errs <- e
			}()
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil && !errors.Is(e, command.ErrForbidden) && !errors.Is(e, command.ErrConflict) && !errors.Is(e, command.ErrVersionConflict) {
					t.Fatal(e)
				}
			}
			if appRow(a.ID).Status == "active" {
				t.Fatal("处罚后申请仍有效")
			}
			var n int64
			must(root.Model(&entity.Penalty{}).Where("account_id=?", oi).Count(&n).Error)
			if n != 1 {
				t.Fatal("并发处罚丢失")
			}
		}
	})
	t.Run("拉黑与同意并发依统一账号锁串行", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			oi, ot := student()
			_, ht := student()
			w := published(ot)
			a := apply(ht, w, "并发合成留言")
			scan("application", a.ID)
			ar := appRow(a.ID)
			wr := wishRow(w.ID)
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, e := students.Execute(ctx, ht, uuid.NewString(), "block-race", "block", "", safety.Input{TargetUserID: oi})
				errs <- e
			}()
			go func() {
				defer wg.Done()
				_, e := wishes.AcceptApplication(ctx, ot, uuid.NewString(), "accept-race", a.ID, wish.AcceptInput{ExpectedVersion: ar.Version, WishExpectedVersion: wr.Version})
				errs <- e
			}()
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil && !errors.Is(e, command.ErrConflict) && !errors.Is(e, command.ErrVersionConflict) && !errors.Is(e, command.ErrBlockedInteraction) {
					t.Fatal(e)
				}
			}
			var n int64
			must(root.Model(&entity.Match{}).Where("wish_id=? AND status='current'", w.ID).Count(&n).Error)
			if n > 1 {
				t.Fatal("重复匹配")
			}
			if appRow(a.ID).Status == "active" {
				t.Fatal("拉黑后旧申请仍可同意")
			}
		}
	})
}
