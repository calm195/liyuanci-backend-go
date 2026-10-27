package safety

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// impose 处置时长仅由等级决定；同违规同动作跨幂等键也不重复处罚或通知。
func (s *Service) impose(ctx context.Context, tx *command.Tx, id string, in Input) (string, error) {
	db := tx.DB().WithContext(ctx)
	action := map[string]string{"minor": "warning", "general": "mute", "severe": "ban"}[in.Level]
	var old []entity.Penalty
	if e := db.Where("account_id=? AND violation_ref=? AND action_kind=?", id, in.ViolationRef, action).Find(&old).Error; e != nil {
		return "", e
	}
	if len(old) > 0 {
		if old[0].ContentID == nil || *old[0].ContentID != in.ContentRef || old[0].ContentVersion != in.ContentVersion {
			return "", command.ErrConflict
		}
		return old[0].ID, nil
	}
	p := entity.Penalty{ID: uuid.NewString(), AccountID: id, Level: in.Level, ActionKind: action, ViolationRef: in.ViolationRef, ContentVersion: in.ContentVersion, ContentID: &in.ContentRef, StartsAt: tx.Now(), ReasonRef: uuid.NewString()}
	if action == "mute" {
		end := tx.Now().Add(168 * time.Hour)
		p.EndsAt = &end
	}
	var e error
	p.ReasonCipher, e = s.encrypt(ctx, "penalty_reason", p.ID, in.Reason)
	if e != nil {
		return "", e
	}
	if e = db.Create(&p).Error; e != nil {
		return "", e
	}
	c, e := registry(db, in.ContentRef)
	if e != nil {
		return "", e
	}
	if e = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entity.ContentRestriction{ID: uuid.NewString(), ContentID: c.ID, Kind: "violation", SourceRef: p.ViolationRef, Active: true}).Error; e != nil {
		return "", e
	}
	// 违规内容永久离开当前公开/送达视图；解罚只撤账号处罚，内容限制由独立审核入口处理。
	switch c.Kind {
	case "wish":
		if e = db.Model(&entity.Wish{}).Where("id=? AND current_content_version=? AND state IN ('published','ready')", c.EntityID, c.ContentVersion).Updates(map[string]any{"state": "withdrawn", "version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1")}).Error; e != nil {
			return "", e
		}
	case "story":
		if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", c.EntityID, c.ContentVersion).Update("public_body", nil).Error; e != nil {
			return "", e
		}
	case "application":
		var a entity.Application
		if e = db.Where("id=?", c.EntityID).Take(&a).Error; e != nil {
			return "", e
		}
		if a.Status != "accepted" {
			if e = wish.CancelApplication(ctx, db, a, "invalidated", "content_violation"); e != nil {
				return "", e
			}
		}
	case "message":
		var m entity.Message
		if e = db.Where("id=?", c.EntityID).Take(&m).Error; e != nil {
			return "", e
		}
		if m.Status == "pending_scan" || m.Status == "pending_review" {
			if e = chat.CancelMessage(ctx, db, m); e != nil {
				return "", e
			}
		}
		// 已送达序号保留；读取侧按内容限制过滤正文，不回退送达计数器。
	}
	if action != "warning" {
		if e = cancelRelationships(ctx, tx, id, ""); e != nil {
			return "", e
		}
		if e = db.Model(&entity.Wish{}).Where("owner_id=? AND draft_deleted_at IS NULL AND state='published'", id).Updates(map[string]any{"state": "withdrawn", "version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1")}).Error; e != nil {
			return "", e
		}
		// 不递增auth_epoch：原本人会话仍可进入申诉/警告限定入口。旧审核通过状态取消保障不重放。
		if e = db.Model(&entity.Account{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error; e != nil {
			return "", e
		}
		var tasks []entity.ModerationTask
		if e = db.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.author_id=? AND c.kind<>'whisper_capsule' AND t.status IN ('pending','human_required')", id).Find(&tasks).Error; e != nil {
			return "", e
		}
		for _, t := range tasks {
			if e = db.Model(&entity.ModerationTask{}).Where("id=?", t.ID).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
		}
	}
	if e = audit(db, tx.Actor().ID, p.ID, "penalty.impose"); e != nil {
		return "", e
	}
	return p.ID, notify(db, id, "penalty_notice", p.ID)
}
