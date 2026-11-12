package privacy

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// activateWishComments 在注销受理时同步隐藏评论及系统引用，不等待异步清理。
func activateWishComments(ctx context.Context, db *gorm.DB, id string) error {
	db = db.WithContext(ctx)
	var affected []entity.WishComment
	if e := db.Where(`author_id=? OR wish_id IN(SELECT id FROM public.wishes WHERE owner_id=?)
 OR root_comment_id IN(SELECT id FROM public.wish_comments WHERE author_id=?)
 OR reply_to_comment_id IN(SELECT id FROM public.wish_comments WHERE author_id=?)`, id, id, id, id).Find(&affected).Error; e != nil {
		return e
	}
	ids := []string{}
	for _, c := range affected {
		ids = append(ids, c.ID)
		if e := hold(db, "wish_comment", c.ID, c.ContentVersion+1); e != nil {
			return e
		}
	}
	if len(ids) > 0 {
		if e := db.Model(&entity.WishComment{}).Where("id IN ?", ids).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		if e := db.Model(&entity.WishCommentVersion{}).Where("comment_id IN ?", ids).Update("public_body", nil).Error; e != nil {
			return e
		}
		if e := db.Exec("UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE status IN('pending','human_required','machine_passed') AND content_id IN(SELECT id FROM public.content_registry WHERE kind='wish_comment' AND entity_id IN ?)", ids).Error; e != nil {
			return e
		}
		if e := db.Exec("DELETE FROM public.notifications WHERE id IN(SELECT notification_id FROM public.content_comment_notices WHERE comment_id IN ?)", ids).Error; e != nil {
			return e
		}
	}
	return db.Where("account_id=?", id).Delete(&entity.ContentFavorite{}).Error
}

// wishCommentsPlan 将评论链的全部主体纳入注销锁计划，防止提交审核与注销交错。
func wishCommentsPlan(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	var rows []entity.WishComment
	err = db.WithContext(ctx).Where(`author_id=? OR wish_id IN(SELECT id FROM public.wishes WHERE owner_id=?) OR root_comment_id IN(SELECT id FROM public.wish_comments WHERE author_id=?) OR reply_to_comment_id IN(SELECT id FROM public.wish_comments WHERE author_id=?)`, id, id, id, id).Find(&rows).Error
	if err != nil {
		return
	}
	for _, row := range rows {
		q, e := wish.DiscoverWishComment(ctx, db, row.WishID, row.ID, row.RootCommentID, row.ReplyToCommentID)
		if e != nil {
			return p, e
		}
		p = merge(p, q)
	}
	return
}
