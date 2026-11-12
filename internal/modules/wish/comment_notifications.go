package wish

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// CommentNotificationTarget 与公开评论共用父帖、作者、楼内引用及治理屏障，不在通知中泄漏匿名账号。
func CommentNotificationTarget(ctx context.Context, db *gorm.DB, notice entity.Notification, now time.Time) (wishID, commentID string, err error) {
	if notice.Type != "wish_comment" {
		return
	}
	var rows []struct{ WishID, CommentID string }
	err = visibleWishComments(ctx, db, now).
		Select("c.wish_id,c.id AS comment_id").
		Joins("JOIN public.content_comment_notices n ON n.comment_id=c.id").
		Where("n.notification_id=? AND n.recipient_id=?", notice.ID, notice.RecipientID).
		Scan(&rows).Error
	if err == nil && len(rows) == 1 {
		wishID, commentID = rows[0].WishID, rows[0].CommentID
	}
	return
}
