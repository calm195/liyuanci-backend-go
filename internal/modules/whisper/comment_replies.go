package whisper

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func replyShape(root, reply *string) bool {
	return root == nil && reply == nil || root != nil && reply != nil && ValidID(*root) && ValidID(*reply)
}
func discoverReply(ctx context.Context, db *gorm.DB, p *command.Discovery, root, reply *string) error {
	for _, id := range []*string{root, reply} {
		if id == nil {
			continue
		}
		var row entity.WhisperComment
		if e := db.WithContext(ctx).Select("id,author_id").Where("id=?", *id).Take(&row).Error; e != nil {
			return missingRow(e)
		}
		p.Accounts = append(p.Accounts, row.AuthorID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperComment, ID: row.ID})
	}
	return nil
}

// ReplyContextEligible 供评论写入及审核提交时复核同帖、同根、当前版本与可见性。
func ReplyContextEligible(ctx context.Context, db *gorm.DB, c entity.WhisperComment, now time.Time) (bool, error) {
	if !replyShape(c.RootCommentID, c.ReplyToCommentID) {
		return false, nil
	}
	if c.RootCommentID == nil {
		return true, nil
	}
	if c.PostID == nil {
		return false, nil
	}
	var root, target entity.WhisperComment
	for _, spec := range []struct {
		id  string
		row *entity.WhisperComment
	}{{*c.RootCommentID, &root}, {*c.ReplyToCommentID, &target}} {
		if e := db.WithContext(ctx).Select("id,author_id,root_comment_id").Where("id=? AND post_id=? AND parent_content_version=?", spec.id, *c.PostID, c.ParentContentVersion).Take(spec.row).Error; errors.Is(e, gorm.ErrRecordNotFound) {
			return false, nil
		} else if e != nil {
			return false, e
		}
		var n int64
		if e := visibleCommentsQuery(ctx, db, *c.PostID, c.ParentContentVersion, now).Where("c.id=?", spec.id).Count(&n).Error; e != nil {
			return false, e
		}
		if n != 1 {
			return false, nil
		}
		if e := interactionBlocked(ctx, db, c.AuthorID, spec.row.AuthorID); errors.Is(e, command.ErrBlockedInteraction) {
			return false, nil
		} else if e != nil {
			return false, e
		}
	}
	return root.RootCommentID == nil && (target.ID == root.ID || target.RootCommentID != nil && *target.RootCommentID == root.ID), nil
}
