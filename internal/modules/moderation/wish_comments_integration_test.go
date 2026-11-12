package moderation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/safety"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// verifyWishComments 在正式命令、审核worker和受限数据库角色上验证评论整条链路。
func verifyWishComments(t *testing.T, db *gorm.DB, s *wish.Service, reviews *moderation.Service, chats *chat.Service, governance *safety.Service, admin string, student func() (string, string), published func(string) wish.CreatedDraft, scan func(string, string), worker *moderation.Worker, event func(string, string) durable.Event) {
	t.Run("心愿评论补充楼中楼审核与上下文失效", func(t *testing.T) {
		ctx := context.Background()
		must := func(e error) {
			t.Helper()
			if e != nil {
				t.Fatal(e)
			}
		}
		owner, ownerToken := student()
		peer, peerToken := student()
		_, thirdToken := student()
		w := published(ownerToken)
		input := func(body string) wish.CommentInput {
			return wish.CommentInput{Body: body, ExpectedPostVersion: w.Version, ExpectedCommentsEpoch: 1}
		}
		create := func(token string, in wish.CommentInput) wish.Comment {
			t.Helper()
			c, e := s.SaveComment(ctx, token, uuid.NewString(), "comment-test", w.ID, "", in)
			must(e)
			return c
		}
		first := create(peerToken, input("想了解更多信息"))
		page, e := s.ListComments(ctx, "", "", w.ID, false, wish.CommentQuery{})
		must(e)
		if len(page.Items) != 0 {
			t.Fatal("待审评论泄漏")
		}
		scan("wish_comment", first.ID)
		first, e = s.GetComment(ctx, peerToken, "", first.ID)
		must(e)
		if first.Status != "published" {
			t.Fatal("评论未自动公开", first.Status)
		}
		replyInput := input("我也有同样的问题")
		replyInput.RootCommentID = &first.ID
		replyInput.ReplyToCommentID = &first.ID
		reply := create(thirdToken, replyInput)
		scan("wish_comment", reply.ID)
		replies, e := s.ListComments(ctx, "", "", w.ID, false, wish.CommentQuery{RootCommentID: first.ID})
		must(e)
		if len(replies.Items) != 1 || replies.Items[0].ID != reply.ID {
			t.Fatal("楼内回复不正确")
		}
		supp := input("补充说明活动地点")
		supp.Kind = "author_supplement"
		if _, e = s.SaveComment(ctx, peerToken, uuid.NewString(), "", w.ID, "", supp); !errors.Is(e, command.ErrForbidden) {
			t.Fatal("非作者补充未拒绝", e)
		}
		supplement := create(ownerToken, supp)
		scan("wish_comment", supplement.ID)
		public, e := s.PublicInteractionContent(ctx, ownerToken, "", w.ID)
		must(e)
		if public.CommentCount != 2 || public.SupplementCount != 1 {
			t.Fatal("评论补充统计混淆", public.CommentCount, public.SupplementCount)
		}
		var notices int64
		must(db.Model(&entity.ContentCommentNotice{}).Where("recipient_id=? AND comment_id IN ?", owner, []string{first.ID, reply.ID, supplement.ID}).Count(&notices).Error)
		if notices != 2 {
			t.Fatal("作者通知去重错误", notices)
		}
		summary, e := chats.NotificationSummary(ctx, ownerToken, "")
		must(e)
		if summary.UnreadCount < 2 {
			t.Fatal("未读未含评论")
		}
		// 通知定位必须继承根楼作者的实时处罚；既有未读条目仍保留通用提示。
		starts := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
		ends := starts.Add(168 * time.Hour)
		penalty := entity.Penalty{ID: uuid.NewString(), AccountID: peer, Level: "general", ActionKind: "mute", ViolationRef: uuid.NewString(), ContentVersion: 1, StartsAt: starts, EndsAt: &ends, ReasonRef: uuid.NewString()}
		must(db.Create(&penalty).Error)
		notifications, e := chats.Notifications(ctx, ownerToken, "", chat.Query{Limit: 50})
		must(e)
		found := 0
		for _, notice := range notifications.Items {
			if notice.Kind == "wish_comment" {
				found++
				if notice.TargetRef != nil || notice.CommentID != nil {
					t.Fatal("通知泄漏已隐藏评论定位", notice)
				}
			}
		}
		if found != 2 {
			t.Fatal("通用未读评论通知丢失", found)
		}
		must(db.Delete(&penalty).Error)
		old := create(peerToken, input("屏蔽之前已提交"))
		_, e = governance.Execute(ctx, ownerToken, uuid.NewString(), "", "block", "", safety.Input{TargetUserID: peer})
		must(e)
		var block entity.Block
		must(db.Where("owner_id=? AND target_id=?", owner, peer).Take(&block).Error)
		_, e = governance.Execute(ctx, ownerToken, uuid.NewString(), "", "unblock", peer, safety.Input{ExpectedVersion: block.Version})
		must(e)
		_, e = worker.Consume(ctx, event("wish_comment", old.ID))
		must(e)
		old, e = s.GetComment(ctx, peerToken, "", old.ID)
		must(e)
		if old.Status != "cancelled" {
			t.Fatal("解除屏蔽使旧心愿评论复活", old.Status)
		}
		first, e = s.WithdrawComment(ctx, peerToken, uuid.NewString(), "", first.ID, first.Version)
		must(e)
		roots, e := s.ListComments(ctx, "", "", w.ID, false, wish.CommentQuery{})
		must(e)
		if len(roots.Items) != 1 || roots.Items[0].Body == nil || *roots.Items[0].Body != "该评论已删除" || roots.Items[0].ReplyCount != 1 {
			t.Fatal("已删根楼未保留安全占位", roots)
		}
		pending := create(peerToken, input("普通命中 待人工复核"))
		scan("wish_comment", pending.ID)
		var task entity.ModerationTask
		must(db.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry r ON r.id=t.content_id").Where("r.kind='wish_comment' AND r.entity_id=?", pending.ID).Take(&task).Error)
		detail, e := reviews.Case(ctx, admin, "", task.ID)
		must(e)
		outcome, e := reviews.Decide(ctx, admin, uuid.NewString(), "", task.ID, moderation.Decision{ExpectedVersion: detail.Version, Decision: "approved"})
		must(e)
		if outcome.BusinessOutcome != "published" {
			t.Fatal("人工评论审核返回错误状态", outcome)
		}
		updated, e := s.GetComment(ctx, peerToken, "", pending.ID)
		must(e)
		edit := input("更新后再次审核")
		edit.ExpectedVersion = updated.Version
		updated, e = s.SaveComment(ctx, peerToken, uuid.NewString(), "", w.ID, pending.ID, edit)
		must(e)
		_, e = s.SetComments(ctx, ownerToken, uuid.NewString(), "", w.ID, w.Version, false)
		must(e)
		if _, e = s.SaveComment(ctx, peerToken, uuid.NewString(), "", w.ID, "", input("迟到版本")); !errors.Is(e, command.ErrVersionConflict) {
			t.Fatal("旧版本未保留409错误", e)
		}
		closed := input("评论已关闭")
		closed.ExpectedPostVersion = w.Version + 1
		closed.ExpectedCommentsEpoch = 2
		if _, e = s.SaveComment(ctx, peerToken, uuid.NewString(), "", w.ID, "", closed); !errors.Is(e, command.ErrBlockedInteraction) {
			t.Fatal("关评未保留互动错误", e)
		}
		_, e = worker.Consume(ctx, event("wish_comment", updated.ID))
		must(e)
		current, e := s.GetComment(ctx, peerToken, "", updated.ID)
		must(e)
		if current.Status == "published" {
			t.Fatal("关闭评论后迟到审核重新公开")
		}
	})
	t.Run("同一评论改稿纳入提交预算", func(t *testing.T) {
		ctx := context.Background()
		_, token := student()
		w := published(token)
		input := wish.CommentInput{Body: "合成限流评论", ExpectedPostVersion: w.Version, ExpectedCommentsEpoch: 1}
		c, e := s.SaveComment(ctx, token, uuid.NewString(), "", w.ID, "", input)
		if e != nil {
			t.Fatal(e)
		}
		for range 19 {
			input.ExpectedVersion = c.Version
			c, e = s.SaveComment(ctx, token, uuid.NewString(), "", w.ID, c.ID, input)
			if e != nil {
				t.Fatal(e)
			}
		}
		input.ExpectedVersion = c.Version
		if _, e = s.SaveComment(ctx, token, uuid.NewString(), "", w.ID, c.ID, input); !errors.Is(e, command.ErrConflict) {
			t.Fatal("反复改稿绕过提交预算", e)
		}
		var versions int64
		if e = db.Model(&entity.WishCommentVersion{}).Where("comment_id=?", c.ID).Count(&versions).Error; e != nil || versions != 20 {
			t.Fatal("拒绝后仍创建审核版本", versions, e)
		}
	})
}
