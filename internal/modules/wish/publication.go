package wish

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ApplyApprovedPublication 在审核任务决定已落库的同一事务中兑现当前版本发布意愿。
// 调用者必须持有作者及心愿锁；历史无发布意愿内容保留ready，不因迁移被公开。
func ApplyApprovedPublication(ctx context.Context, db *gorm.DB, id string, contentVersion int64) (State, error) {
	a, err := readAggregate(ctx, db, id, false)
	if err != nil {
		return "", err
	}
	if !a.row.PublishIntent || a.snapshot.State != Ready || a.row.CurrentContentVersion != contentVersion {
		return a.snapshot.State, nil
	}
	if !ordinary(a.owner, a.snapshot) || !reviewReady(a.snapshot) {
		return a.snapshot.State, nil
	}
	if err = validateBoundImages(ctx, db, id, contentVersion); err != nil {
		return "", err
	}
	var now time.Time
	if err = db.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return "", err
	}
	state := Published
	expiry := a.row.ExpiresAt
	first := a.row.FirstPublishedAt
	if !a.row.RefreshOnPublish && expiry != nil && !now.Before(*expiry) {
		state = Expired
	} else {
		if expiry == nil || a.row.RefreshOnPublish {
			v := now.Add(time.Duration(a.row.DurationDays) * 24 * time.Hour)
			expiry = &v
		}
		if first == nil && a.row.PublishedAt == nil {
			first = &now
		}
	}
	updates := map[string]any{"state": state, "publish_intent": false, "version": gorm.Expr("version+1")}
	if state == Published {
		updates["expires_at"] = expiry
		updates["published_at"] = now
		updates["first_published_at"] = first
		updates["refresh_on_publish"] = false
	}
	changed := db.Model(&entity.Wish{}).Where("id=? AND current_content_version=? AND state='ready' AND publish_intent", id, contentVersion).Updates(updates)
	if changed.Error != nil {
		return "", changed.Error
	}
	if changed.RowsAffected != 1 {
		return "", command.ErrVersionConflict
	}
	if state == Published {
		err = db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: a.row.OwnerID, Type: "wish_published", TargetRef: id}).Error
	}
	return state, err
}
