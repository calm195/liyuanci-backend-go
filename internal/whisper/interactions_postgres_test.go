package whisper_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// testWhisperInteractions 复用独立数据库与受限身份，验证B05真实命令、审核及HTTP闭环。
func testWhisperInteractions(t *testing.T, admin, api *gorm.DB, s *whisper.Service, w *moderation.WhisperWorker, analyzer *reviewAnalyzer, actor, other, token, otherToken string, legacy *chat.Service, governance *safety.Service, reviews *moderation.Service, adminToken string) {
	t.Helper()
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	event := func(id string) durable.Event {
		var ev durable.Event
		must(admin.Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.entity_id=? ORDER BY t.created_at DESC,t.id DESC LIMIT 1`, id).Scan(&ev).Error)
		return ev
	}
	consume := func(id string) { _, e := w.Consume(ctx, event(id)); must(e) }
	createPost := func() whisper.PublicPost {
		p, e := s.CreatePost(ctx, token, uuid.NewString(), "", whisper.ContentInput{Body: "互动测试父帖", Identity: whisper.IdentityInput{Mode: "anonymous"}})
		must(e)
		_, e = s.ChangePost(ctx, token, uuid.NewString(), "", p.ID, "submit", whisper.PostInput{ExpectedVersion: p.Version})
		must(e)
		consume(p.ID)
		raw, e := s.GetPost(ctx, token, "", p.ID, false)
		must(e)
		return raw.(whisper.PublicPost)
	}
	current := func(id string) whisper.PublicPost {
		raw, e := s.GetPost(ctx, token, "", id, false)
		must(e)
		return raw.(whisper.PublicPost)
	}
	router := httpapi.New(zap.NewNop(), nil)
	must(httpapi.RegisterWhispers(router, s))
	must(httpapi.RegisterWhisperInteractions(router, s))
	samples := []map[string]any{}
	request := func(op, method, path, tok string, body any, status int, key ...string) json.RawMessage {
		t.Helper()
		var data []byte
		if raw, ok := body.(string); ok {
			data = []byte(raw)
		} else if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(data))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("Content-Type", "application/json")
		k := uuid.NewString()
		if len(key) > 0 {
			k = key[0]
		}
		req.Header.Set("Idempotency-Key", k)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s: status=%d want=%d: %s", op, rec.Code, status, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
			t.Fatal("缺少禁止缓存头")
		}
		var response any
		must(json.Unmarshal(rec.Body.Bytes(), &response))
		if op != "" {
			samples = append(samples, map[string]any{"operation_id": op, "status": status, "response": response})
		}
		var out struct {
			Data json.RawMessage `json:"data"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &out))
		return out.Data
	}
	createComment := func(p whisper.PublicPost, body string) whisper.OwnerComment {
		v, e := s.SaveComment(ctx, otherToken, uuid.NewString(), "", p.ID, "", whisper.CommentInput{ContentInput: whisper.ContentInput{Body: body, Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedPostVersion: p.Version, ExpectedCommentsEpoch: p.CommentsEpoch})
		must(e)
		return v
	}
	t.Run("未实名暖评与十个HTTP操作", func(t *testing.T) {
		p := createPost()
		must(admin.Exec("UPDATE public.accounts SET verification='unverified' WHERE id=?", other).Error)
		body := map[string]any{"expected_post_version": p.Version, "expected_comments_epoch": p.CommentsEpoch, "body": "合成人名的暖评", "identity": map[string]any{"mode": "anonymous"}}
		raw := request("whisperCommentCreate", "POST", "/whispers/posts/"+p.ID+"/comments", otherToken, body, 202)
		var c whisper.OwnerComment
		must(json.Unmarshal(raw, &c))
		consume(c.ID)
		raw = request("whisperCommentsList", "GET", "/whispers/posts/"+p.ID+"/comments", "", nil, 200)
		var page whisper.Page[whisper.PublicComment]
		must(json.Unmarshal(raw, &page))
		if len(page.Items) != 1 || page.Items[0].Body != "合成人名的暖评" || page.Items[0].Identity.IsAuthor {
			t.Fatal(page)
		}
		if strings.Contains(string(raw), other) {
			t.Fatal("公开响应包含账号")
		}
		p = current(p.ID)
		if p.CommentCount != 1 {
			t.Fatal(p)
		}
		request("whisperOwnCommentsList", "GET", "/me/whispers/comments", otherToken, nil, 200)
		request("whisperOwnCommentGet", "GET", "/me/whispers/comments/"+c.ID, otherToken, nil, 200)
		owner, e := s.GetComment(ctx, otherToken, "", c.ID)
		must(e)
		update := map[string]any{"expected_version": owner.Version, "expected_post_version": p.Version, "expected_comments_epoch": p.CommentsEpoch, "body": "改稿暖评", "identity": map[string]any{"mode": "anonymous"}}
		raw = request("whisperCommentUpdate", "PUT", "/me/whispers/comments/"+c.ID, otherToken, update, 202)
		must(json.Unmarshal(raw, &c))
		consume(c.ID)
		raw = request("whisperNotificationsList", "GET", "/me/whispers/notifications", token, nil, 200)
		var notices whisper.Page[whisper.Notification]
		must(json.Unmarshal(raw, &notices))
		var notice whisper.Notification
		count := 0
		for _, n := range notices.Items {
			if n.TargetID == p.ID {
				notice = n
				count++
			}
		}
		if count != 1 {
			t.Fatal("重审重复通知", notices)
		}
		request("whisperNotificationRead", "POST", "/me/whispers/notifications/"+notice.ID+"/read", token, map[string]any{"expected_version": notice.Version}, 200)
		_, e = legacy.ReadNotification(ctx, token, uuid.NewString(), "", notice.ID, notice.Version)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal("旧已读入口泄漏", e)
		}
		old, e := legacy.Notifications(ctx, token, "", chat.Query{Limit: 50})
		must(e)
		for _, n := range old.Items {
			if n.ID == notice.ID {
				t.Fatal("旧列表泄漏")
			}
		}
		request("whisperNotificationsList", "GET", "/me/whispers/notifications", otherToken, nil, 200)
		_, e = s.ReadNotification(ctx, otherToken, uuid.NewString(), "", notice.ID, 1)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		c, e = s.GetComment(ctx, otherToken, "", c.ID)
		must(e)
		request("whisperCommentWithdraw", "POST", "/me/whispers/comments/"+c.ID+"/withdraw", otherToken, map[string]any{"expected_version": c.Version}, 200)
		p = current(p.ID)
		request("whisperCommentsSetting", "PUT", "/me/whispers/posts/"+p.ID+"/comments-setting", token, map[string]any{"expected_version": p.Version, "comments_open": false}, 200)
		p = current(p.ID)
		request("whisperHugSet", "PUT", "/whispers/posts/"+p.ID+"/hugs", otherToken, map[string]any{"expected_post_version": p.Version, "expected_hug_version": 0, "active": true}, 200)
		for _, bad := range []string{`{"expected_post_version":1,"expected_comments_epoch":1,"body":"正文","body":"重复","identity":{"mode":"anonymous"}}`, `{"expected_post_version":1,"expected_comments_epoch":1,"body":null,"identity":{"mode":"anonymous"}}`, `{"expected_post_version":1,"expected_comments_epoch":1,"body":"正文","identity":{"mode":"anonymous"},"extra":1}`} {
			request("whisperCommentCreate", "POST", "/whispers/posts/"+p.ID+"/comments", otherToken, bad, 400)
		}
		request("whisperOwnCommentGet", "GET", "/me/whispers/comments/"+c.ID, token, nil, 404)
		request("whisperOwnCommentsList", "GET", "/me/whispers/comments", "", nil, 401)
	})
	t.Run("开关批准与父正文换版", func(t *testing.T) {
		p := createPost()
		first := createComment(p, "已有公开暖评")
		consume(first.ID)
		pending := createComment(p, "普通命中 迟到审核")
		analyzer.hook = func() { _, e := s.SetComments(ctx, token, uuid.NewString(), "", p.ID, p.Version, false); must(e) }
		result, e := w.Consume(ctx, event(pending.ID))
		analyzer.hook = nil
		must(e)
		if !result.Cancelled {
			t.Fatal(result)
		}
		raw, e := s.ListComments(ctx, "", "", p.ID, false, whisper.Query{Limit: 20})
		must(e)
		if len(raw.(whisper.Page[whisper.PublicComment]).Items) != 1 {
			t.Fatal("关闭隐藏既有公开项", raw)
		}
		p = current(p.ID)
		_, e = s.SetComments(ctx, token, uuid.NewString(), "", p.ID, p.Version, true)
		must(e)
		consume(pending.ID)
		raw, e = s.ListComments(ctx, "", "", p.ID, false, whisper.Query{Limit: 20})
		must(e)
		if len(raw.(whisper.Page[whisper.PublicComment]).Items) != 1 {
			t.Fatal("恢复补发取消项")
		}
		p = current(p.ID)
		changed, e := s.ChangePost(ctx, token, uuid.NewString(), "", p.ID, "update", whisper.PostInput{ExpectedVersion: p.Version, ContentInput: whisper.ContentInput{Body: "新父正文", Identity: whisper.IdentityInput{Mode: "anonymous"}}})
		must(e)
		c, e := s.GetComment(ctx, otherToken, "", first.ID)
		must(e)
		if c.Status != "context_changed" {
			t.Fatal(c)
		}
		_, e = s.ChangePost(ctx, token, uuid.NewString(), "", p.ID, "submit", whisper.PostInput{ExpectedVersion: changed.Version})
		must(e)
		consume(p.ID)
		p = current(p.ID)
		if p.CommentCount != 0 {
			t.Fatal("旧评论串跨正文恢复")
		}
		c, e = s.SaveComment(ctx, otherToken, uuid.NewString(), "", "", c.ID, whisper.CommentInput{ContentInput: whisper.ContentInput{Body: "显式重提", Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedVersion: c.Version, ExpectedPostVersion: p.Version, ExpectedCommentsEpoch: p.CommentsEpoch})
		must(e)
		consume(c.ID)
		p = current(p.ID)
		if p.CommentCount != 1 {
			t.Fatal(p)
		}
		_, e = s.ChangePost(ctx, token, uuid.NewString(), "", p.ID, "withdraw", whisper.PostInput{ExpectedVersion: p.Version})
		must(e)
		rawOwn, e := s.GetPost(ctx, token, "", p.ID, true)
		must(e)
		_, e = s.ChangePost(ctx, token, uuid.NewString(), "", p.ID, "submit", whisper.PostInput{ExpectedVersion: rawOwn.(whisper.OwnerPost).Version})
		must(e)
		consume(p.ID)
		if current(p.ID).CommentCount != 1 {
			t.Fatal("同正文重审没有恢复合规评论")
		}
	})
	t.Run("抱抱目标态重放拉黑及并发", func(t *testing.T) {
		p := createPost()
		key := uuid.NewString()
		in := whisper.HugInput{ExpectedPostVersion: p.Version, ExpectedHugVersion: 0, Active: true}
		h, e := s.SetHug(ctx, otherToken, key, "", p.ID, in)
		must(e)
		if h.MyHug.Version != 1 || h.HugCount != 1 {
			t.Fatal(h)
		}
		h, e = s.SetHug(ctx, otherToken, uuid.NewString(), "", p.ID, whisper.HugInput{ExpectedPostVersion: p.Version, ExpectedHugVersion: 1, Active: false})
		must(e)
		replay, e := s.SetHug(ctx, otherToken, key, "", p.ID, in)
		must(e)
		if replay.MyHug.Active || replay.MyHug.Version != 2 {
			t.Fatal(replay)
		}
		_, e = s.SetHug(ctx, otherToken, uuid.NewString(), "", p.ID, in)
		if !errors.Is(e, command.ErrVersionConflict) {
			t.Fatal(e)
		}
		_, e = s.SetHug(ctx, token, uuid.NewString(), "", p.ID, in)
		if !errors.Is(e, command.ErrBlockedInteraction) {
			t.Fatal(e)
		}
		must(admin.Create(&entity.Block{OwnerID: actor, TargetID: other}).Error)
		_, e = s.SetHug(ctx, otherToken, uuid.NewString(), "", p.ID, whisper.HugInput{ExpectedPostVersion: p.Version, ExpectedHugVersion: 2, Active: true})
		if !errors.Is(e, command.ErrBlockedInteraction) {
			t.Fatal(e)
		}
		_, e = s.SetHug(ctx, otherToken, uuid.NewString(), "", p.ID, whisper.HugInput{ExpectedPostVersion: p.Version, ExpectedHugVersion: 2, Active: false})
		must(e)
		_, e = s.SaveComment(ctx, otherToken, uuid.NewString(), "", p.ID, "", whisper.CommentInput{ContentInput: whisper.ContentInput{Body: "关系受限", Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedPostVersion: p.Version, ExpectedCommentsEpoch: p.CommentsEpoch})
		if !errors.Is(e, command.ErrBlockedInteraction) {
			t.Fatal(e)
		}
		must(admin.Where("owner_id=? AND target_id=?", actor, other).Delete(&entity.Block{}).Error)
		p2 := createPost()
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := s.SetHug(ctx, otherToken, uuid.NewString(), "", p2.ID, whisper.HugInput{ExpectedPostVersion: p2.Version, ExpectedHugVersion: 0, Active: true})
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		success, conflict := 0, 0
		for e := range errs {
			if e == nil {
				success++
			} else if errors.Is(e, command.ErrVersionConflict) {
				conflict++
			} else {
				t.Fatal(e)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatal(success, conflict)
		}
		must(admin.Exec("UPDATE public.accounts SET lifecycle='deleting' WHERE id=?", other).Error)
		if current(p2.ID).HugCount != 0 {
			t.Fatal("注销屏障未立即过滤计数")
		}
		must(admin.Exec("UPDATE public.accounts SET lifecycle='active' WHERE id=?", other).Error)
	})
	t.Run("自评论静默与隐私父屏障", func(t *testing.T) {
		p := createPost()
		c, e := s.SaveComment(ctx, token, uuid.NewString(), "", p.ID, "", whisper.CommentInput{ContentInput: whisper.ContentInput{Body: "帖主自己的评论", Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedPostVersion: p.Version, ExpectedCommentsEpoch: p.CommentsEpoch})
		must(e)
		consume(c.ID)
		var count int64
		must(admin.Model(&entity.WhisperNotificationRef{}).Where("source_id=?", c.ID).Count(&count).Error)
		if count != 0 {
			t.Fatal("自评论产生通知")
		}
		must(admin.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "whisper_post", TargetID: p.ID, HoldEpoch: 1, RequiredContentVersion: 1, Status: "active"}).Error)
		own, e := s.GetComment(ctx, token, "", c.ID)
		must(e)
		if own.Status != "privacy_hold" || own.Body != nil || own.IdentityInput != nil {
			t.Fatal(own)
		}
	})
	t.Run("拉黑解除仍取消旧审核", func(t *testing.T) {
		p := createPost()
		c := createComment(p, "普通命中 拉黑竞争暖评")
		analyzer.hook = func() {
			_, e := governance.Execute(ctx, otherToken, uuid.NewString(), "", "block", "", safety.Input{TargetUserID: actor})
			must(e)
			var block entity.Block
			must(admin.Where("owner_id=? AND target_id=?", other, actor).Take(&block).Error)
			_, e = governance.Execute(ctx, otherToken, uuid.NewString(), "", "unblock", actor, safety.Input{ExpectedVersion: block.Version})
			must(e)
		}
		result, e := w.Consume(ctx, event(c.ID))
		analyzer.hook = nil
		must(e)
		if !result.Cancelled {
			t.Fatal(result)
		}
		if current(p.ID).CommentCount != 0 {
			t.Fatal("拉黑解除复活了旧审核")
		}
	})

	t.Run("人工批准首次通知", func(t *testing.T) {
		p := createPost()
		c := createComment(p, "普通命中 的暖评")
		consume(c.ID)
		var task entity.ModerationTask
		must(admin.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.entity_id=?", c.ID).Take(&task).Error)
		_, e := reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", task.ID, moderation.WhisperReviewCommand{ExpectedVersion: task.Version, ExpectedContentVersion: 1, Decision: "approved"})
		must(e)
		if current(p.ID).CommentCount != 1 {
			t.Fatal("人工批准未公开")
		}
		var count int64
		must(admin.Model(&entity.WhisperNotificationRef{}).Where("source_id=?", c.ID).Count(&count).Error)
		if count != 1 {
			t.Fatal("人工批准未生成唯一通知")
		}
	})

	if output := os.Getenv("APP_WHISPER_INTERACTION_WIRE_OUTPUT"); output != "" {
		b, e := json.MarshalIndent(samples, "", "  ")
		must(e)
		must(os.WriteFile(output, append(b, '\n'), 0600))
	}
	_ = api
}
