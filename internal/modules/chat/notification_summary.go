package chat

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// NotificationSummary 返回与全站通知列表同一访问范围内的未读总数。
type NotificationSummary struct {
	UnreadCount int64 `json:"unread_count"`
}

func notificationScope(db *gorm.DB, actor string, now time.Time) (*gorm.DB, error) {
	q := db.Model(&entity.Notification{}).Where("recipient_id=? AND NOT EXISTS(SELECT 1 FROM public.whisper_notification_refs r WHERE r.notification_id=notifications.id AND r.suppressed)", actor)
	if e := readChatAllowed(db, actor, now); errors.Is(e, command.ErrForbidden) {
		q = q.Where("type IN ('penalty_notice','appeal_result','crisis_care')")
	} else if e != nil {
		return nil, e
	}
	return q, nil
}

// NotificationSummary 不取决于已加载分页，也不重复累加模块专属通知。
func (s *Service) NotificationSummary(ctx context.Context, token, rid string) (out NotificationSummary, err error) {
	defer s.done("notifications.summary", rid, time.Now(), &err)
	err = s.read(ctx, token, func(db *gorm.DB, actor string, now time.Time) error {
		q, e := notificationScope(db, actor, now)
		if e != nil {
			return e
		}
		return q.Where("read_at IS NULL").Count(&out.UnreadCount).Error
	})
	return
}
