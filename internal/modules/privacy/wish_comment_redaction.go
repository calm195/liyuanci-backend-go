package privacy

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

type wishCommentRedaction struct {
	comment entity.WishComment
	body    string
}

// prepareWishCommentRedactions 将注销根楼或被回复作者的身份引用交给受控脱敏器，失败仍保持屏障。
func (s *Service) prepareWishCommentRedactions(ctx context.Context, in Intent) (rows []wishCommentRedaction, err error) {
	if in.Scope != "account" {
		return
	}
	var comments []entity.WishComment
	if err = s.db.WithContext(ctx).Where(`author_id<>? AND wish_id NOT IN(SELECT id FROM public.wishes WHERE owner_id=?) AND (root_comment_id IN(SELECT id FROM public.wish_comments WHERE author_id=?) OR reply_to_comment_id IN(SELECT id FROM public.wish_comments WHERE author_id=?))`, in.Target, in.Target, in.Target, in.Target).Find(&comments).Error; err != nil {
		return
	}
	for _, c := range comments {
		var v entity.WishCommentVersion
		if err = s.db.WithContext(ctx).Where("comment_id=? AND content_version=?", c.ID, c.ContentVersion).Take(&v).Error; err != nil {
			return
		}
		body, e := wish.DecryptWishComment(ctx, s.codec, v)
		if e != nil {
			return nil, e
		}
		body, e = s.redact(ctx, "wish_comment", body)
		if e != nil {
			return nil, e
		}
		rows = append(rows, wishCommentRedaction{c, body})
	}
	return
}

// applyWishCommentRedactions 先校验版本，再清除每个历史原文和审核候选；取消内容不会重新公开。
func (s *Service) applyWishCommentRedactions(ctx context.Context, db *gorm.DB, rows []wishCommentRedaction) error {
	for _, row := range rows {
		var c entity.WishComment
		if e := db.Where("id=?", row.comment.ID).Take(&c).Error; e != nil {
			return e
		}
		if c.Version != row.comment.Version {
			return command.ErrRetry
		}
		var versions []entity.WishCommentVersion
		if e := db.Where("comment_id=?", c.ID).Find(&versions).Error; e != nil {
			return e
		}
		for _, v := range versions {
			raw, e := s.encrypt(ctx, "wish_comment_body", c.ID, v.ContentVersion, row.body)
			if e != nil {
				return e
			}
			if e = db.Model(&entity.WishCommentVersion{}).Where("comment_id=? AND content_version=?", c.ID, v.ContentVersion).Updates(map[string]any{"body_cipher": raw, "public_body": nil}).Error; e != nil {
				return e
			}
		}
		if e := db.Exec("DELETE FROM public.moderation_scans WHERE task_id IN(SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry r ON r.id=t.content_id WHERE r.kind='wish_comment' AND r.entity_id=?)", c.ID).Error; e != nil {
			return e
		}
		if e := db.Exec("UPDATE public.moderation_tasks SET reason=NULL,revision_suggestion=NULL,decision=NULL WHERE content_id IN(SELECT id FROM public.content_registry WHERE kind='wish_comment' AND entity_id=?)", c.ID).Error; e != nil {
			return e
		}
	}
	return nil
}
