package wish

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// ApplicationNotificationTarget 将历史通知引用投影为当前收件人可访问的申请入口。
func (s *Service) ApplicationNotificationTarget(ctx context.Context, db *gorm.DB, row entity.Notification, now time.Time) (kind, ref, title string, err error) {
	db = db.WithContext(ctx)
	appID := row.TargetRef
	if row.Type == "application_accepted" {
		var match entity.Match
		err = db.Where("id=? AND helper_id=?", row.TargetRef, row.RecipientID).Take(&match).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && match.ApplicationID == nil {
			return "", "", "", nil
		}
		if err != nil {
			return "", "", "", err
		}
		appID = *match.ApplicationID
	}
	var app entity.Application
	query := db.Where("id=?", appID)
	if row.Type != "application_active" {
		query = query.Where("applicant_id=?", row.RecipientID)
	}
	if err = query.Take(&app).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", "", nil
	} else if err != nil {
		return "", "", "", err
	}
	if row.Type == "application_active" {
		var owner entity.Wish
		err = db.Where("id=? AND owner_id=?", app.WishID, row.RecipientID).Take(&owner).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", nil
		}
		if err != nil || app.Status != "active" {
			return "", "", "", err
		}
		eligible, err := ApplicationEligible(ctx, db, app, now)
		if err != nil || !eligible {
			return "", "", "", err
		}
		var version entity.WishVersion
		if err = db.Where("wish_id=? AND content_version=?", owner.ID, owner.CurrentContentVersion).Take(&version).Error; err != nil {
			return "", "", "", err
		}
		return "wish_applications", app.WishID, publicTitle(version), nil
	}
	title, err = s.applicationWishTitle(ctx, db, "", app.WishID, row.RecipientID, now)
	return "my_application", app.ID, title, err
}

// ApplicationNoticeReceipt 是当前许愿人可标记已读的通知版本。
type ApplicationNoticeReceipt struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

func unreadApplicationNotices(db *gorm.DB, owner string) *gorm.DB {
	return db.Table("public.notifications AS n").
		Where("n.recipient_id=? AND n.type='application_active' AND n.read_at IS NULL", owner)
}

func unreadApplicationCount(ctx context.Context, db *gorm.DB, wishID, owner string, now time.Time) (int, error) {
	var apps []entity.Application
	err := db.WithContext(ctx).Model(&entity.Application{}).
		Where("wish_id=? AND status='active'", wishID).
		Where("EXISTS (?)", unreadApplicationNotices(db, owner).Select("1").Where("n.target_ref=applications.id")).
		Find(&apps).Error
	if err != nil {
		return 0, err
	}
	count := 0
	for _, app := range apps {
		ok, err := ApplicationEligible(ctx, db, app, now)
		if err != nil {
			return 0, err
		}
		if ok {
			count++
		}
	}
	return count, nil
}
