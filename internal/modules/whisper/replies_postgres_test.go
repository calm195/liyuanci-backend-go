package whisper_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/safety"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

// verifyThreadReplies 在正式评论命令与审核worker之间验证根楼约束、两个收件人与安全撤回占位。
func verifyThreadReplies(t *testing.T, db *gorm.DB, s *whisper.Service, governance *safety.Service, worker *moderation.WhisperWorker, owner, peer string, create func(string) whisper.OwnerPost, event func(string) durable.Event) {
	t.Run("两层回复审核及根楼撤回", func(t *testing.T) {
		ctx := context.Background()
		must := func(e error) {
			t.Helper()
			if e != nil {
				t.Fatal(e)
			}
		}
		p := create("交流考试经验")
		_, e := worker.Consume(ctx, event(p.ID))
		must(e)
		raw, e := s.GetPost(ctx, "", "", p.ID, false)
		must(e)
		post := raw.(whisper.PublicPost)
		in := whisper.CommentInput{ContentInput: whisper.ContentInput{Body: "感谢分享", Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedPostVersion: post.Version, ExpectedCommentsEpoch: post.CommentsEpoch}
		first, e := s.SaveComment(ctx, peer, uuid.NewString(), "", p.ID, "", in)
		must(e)
		_, e = worker.Consume(ctx, event(first.ID))
		must(e)
		in.Body = "欢迎交流"
		in.RootCommentID = &first.ID
		in.ReplyToCommentID = &first.ID
		reply, e := s.SaveComment(ctx, owner, uuid.NewString(), "", p.ID, "", in)
		must(e)
		_, e = worker.Consume(ctx, event(reply.ID))
		must(e)
		raw, e = s.ListComments(ctx, "", "", p.ID, false, whisper.Query{RootCommentID: first.ID})
		must(e)
		if page := raw.(whisper.Page[whisper.PublicComment]); len(page.Items) != 1 || page.Items[0].ID != reply.ID {
			t.Fatal("回复页错误", page)
		}
		// 根楼有屏障时，本人列表和详情也不能成为原稿读取旁路。
		hold := entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "whisper_comment", TargetID: first.ID, HoldEpoch: 1, RequiredContentVersion: 1}
		must(db.Create(&hold).Error)
		own, e := s.GetComment(ctx, owner, "", reply.ID)
		must(e)
		if own.Status != "privacy_hold" || own.Body != nil || own.IdentityInput != nil {
			t.Fatal("根楼隐私屏障未继承", own)
		}
		must(db.Delete(&hold).Error)
		var registry entity.ContentRegistry
		must(db.Where("kind='whisper_comment' AND entity_id=?", first.ID).Take(&registry).Error)
		restriction := entity.ContentRestriction{ID: uuid.NewString(), ContentID: registry.ID, Kind: "violation", SourceRef: uuid.NewString()}
		must(db.Create(&restriction).Error)
		own, e = s.GetComment(ctx, owner, "", reply.ID)
		must(e)
		if own.Status != "restricted" || own.Body != nil || own.IdentityInput != nil {
			t.Fatal("根楼治理屏障未继承", own)
		}
		must(db.Delete(&restriction).Error)

		// 第三人回复根楼作者，屏蔽目标与父帖作者不同，随后立即解除也不能让旧任务通过。
		third, thirdToken := uuid.NewString(), uuid.NewString()
		must(db.Create(&entity.Account{ID: third, Nickname: "合成楼内参与人", Verification: "verified"}).Error)
		hash := sha256.Sum256([]byte(thirdToken))
		must(db.Create(&entity.Session{ID: uuid.NewString(), AccountID: &third, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
		pending, e := s.SaveComment(ctx, thirdToken, uuid.NewString(), "", p.ID, "", in)
		must(e)
		var rootComment entity.WhisperComment
		must(db.Where("id=?", first.ID).Take(&rootComment).Error)
		_, e = governance.Execute(ctx, thirdToken, uuid.NewString(), "", "block", "", safety.Input{TargetUserID: rootComment.AuthorID})
		must(e)
		var block entity.Block
		must(db.Where("owner_id=? AND target_id=?", third, rootComment.AuthorID).Take(&block).Error)
		_, e = governance.Execute(ctx, thirdToken, uuid.NewString(), "", "unblock", rootComment.AuthorID, safety.Input{ExpectedVersion: block.Version})
		must(e)
		_, e = worker.Consume(ctx, event(pending.ID))
		must(e)
		own, e = s.GetComment(ctx, thirdToken, "", pending.ID)
		must(e)
		if own.Status != "cancelled" {
			t.Fatal("解除屏蔽使旧楼内回复复活", own.Status)
		}
		var c entity.WhisperComment
		must(db.Where("id=?", first.ID).Take(&c).Error)
		_, e = s.WithdrawComment(ctx, peer, uuid.NewString(), "", first.ID, c.Version)
		must(e)
		raw, e = s.ListComments(ctx, "", "", p.ID, false, whisper.Query{})
		must(e)
		page := raw.(whisper.Page[whisper.PublicComment])
		if len(page.Items) != 1 || page.Items[0].Body != "该评论已删除" || page.Items[0].ReplyCount != 1 {
			t.Fatal("根楼安全占位丢失", page)
		}
		other := create("另一个讨论")
		_, e = worker.Consume(ctx, event(other.ID))
		must(e)
		raw, e = s.GetPost(ctx, "", "", other.ID, false)
		must(e)
		in.ExpectedPostVersion = raw.(whisper.PublicPost).Version
		if _, e = s.SaveComment(ctx, owner, uuid.NewString(), "", other.ID, "", in); e == nil {
			t.Fatal("跨帖回复通过")
		}
	})
}
