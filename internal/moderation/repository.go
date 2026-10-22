package moderation

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

type current struct {
	task     entity.ModerationTask
	registry entity.ContentRegistry
	wish     entity.Wish
	content  entity.WishVersion
	account  entity.Account
}

func load(db *gorm.DB, id string) (out current, err error) {
	for _, step := range []func() error{
		func() error { return db.Where("id=?", id).Take(&out.task).Error },
		func() error {
			return db.Where("id=? AND kind='wish' AND content_version=?", out.task.ContentID, out.task.ContentVersion).Take(&out.registry).Error
		},
		func() error {
			return db.Where("id=? AND owner_id=?", out.registry.EntityID, out.registry.AuthorID).Take(&out.wish).Error
		},
		func() error {
			return db.Where("wish_id=? AND content_version=?", out.wish.ID, out.task.ContentVersion).Take(&out.content).Error
		},
		func() error { return db.Where("id=?", out.wish.OwnerID).Take(&out.account).Error },
	} {
		if err = step(); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				err = command.ErrNotFound
			}
			return
		}
	}
	return
}
func discover(ctx context.Context, db *gorm.DB, id string) (command.Discovery, error) {
	row, e := load(db.WithContext(ctx), id)
	if errors.Is(e, command.ErrNotFound) {
		return command.Discovery{}, nil
	}
	if e != nil {
		return command.Discovery{}, e
	}
	return command.Discovery{Accounts: []string{row.account.ID}, Resources: []command.Resource{{Kind: command.Wish, ID: row.wish.ID}, {Kind: command.ModerationTask, ID: id}}}, nil
}

// eligible 在账号和资源锁后复核当前任务绑定、资格、处罚及全部历史版本限制，不从旧事件恢复正文。
func eligible(db *gorm.DB, row current) (bool, error) {
	if row.account.Lifecycle != "active" || row.account.Verification != "verified" || row.wish.DraftDeletedAt != nil || row.wish.CurrentContentVersion != row.task.ContentVersion || row.content.ReviewTaskID == nil || *row.content.ReviewTaskID != row.task.ID || row.task.SubjectEpoch != row.account.AuthEpoch || row.task.RestrictionEpoch == nil || *row.task.RestrictionEpoch != row.wish.RestrictionEpoch || row.wish.State != "pending_review" {
		return false, nil
	}
	var n int64
	if e := db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ? AND revoked_at IS NULL AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp())", row.account.ID, []string{"mute", "ban"}).Count(&n).Error; e != nil {
		return false, e
	}
	if n > 0 {
		return false, nil
	}
	if e := db.Model(&entity.ContentRestriction{}).Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind='wish' AND c.entity_id=? AND r.active", row.wish.ID).Count(&n).Error; e != nil {
		return false, e
	}
	if n > 0 {
		return false, nil
	}
	var holds []entity.PrivacyHold
	if e := db.Where("target_kind='wish' AND target_id=?", row.wish.ID).Find(&holds).Error; e != nil {
		return false, e
	}
	var epoch int64
	for _, hold := range holds {
		if hold.HoldEpoch > epoch {
			epoch = hold.HoldEpoch
		}
		if hold.Status == "active" {
			return false, nil
		}
	}
	return epoch == row.task.HoldEpoch, nil
}
func setReview(db *gorm.DB, row current, decision string, body *string, tags []byte) error {
	if e := db.Model(&entity.ContentRegistry{}).Where("id=?", row.registry.ID).Update("status", decision).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", row.wish.ID, row.task.ContentVersion).Updates(map[string]any{"moderation_status": decision, "public_body": body, "identity_tags": json.RawMessage(tags)}).Error; e != nil {
		return e
	}
	state := "ready"
	if decision == "rejected" {
		state = "rejected"
	}
	return db.Model(&entity.Wish{}).Where("id=?", row.wish.ID).Updates(map[string]any{"state": state, "version": gorm.Expr("version+1")}).Error
}
