package safety

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// restrict 只处理普通违规限制，解除操作不改变业务状态、危机原因或隐私屏障。
func (s *Service) restrict(ctx context.Context, tx *command.Tx, op, id string, in Input) (string, error) {
	db := tx.DB().WithContext(ctx)
	c, e := targetRegistry(db, in.TargetKind, id)
	if e != nil {
		return "", e
	}
	if op == "release" {
		var rows []entity.ContentRestriction
		if e = db.Table("public.content_restrictions AS r").Select("r.*").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind=? AND c.entity_id=? AND r.kind='violation' AND r.active", in.TargetKind, id).Find(&rows).Error; e != nil {
			return "", e
		}
		for _, r := range rows {
			cipher, e := s.encrypt(ctx, "content_release_reason", r.ID, in.Reason)
			if e != nil {
				return "", e
			}
			if e = db.Model(&entity.ContentRestriction{}).Where("id=?", r.ID).Updates(map[string]any{"active": false, "release_reason_cipher": cipher, "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
		}
	} else {
		r := entity.ContentRestriction{ID: uuid.NewString(), ContentID: c.ID, Kind: "violation", SourceRef: uuid.NewString(), Active: true}
		r.ReasonCipher, e = s.encrypt(ctx, "content_offline_reason", r.ID, in.Reason)
		if e != nil {
			return "", e
		}
		if e = db.Create(&r).Error; e != nil {
			return "", e
		}
		switch c.Kind {
		case "wish":
			if e = db.Model(&entity.Wish{}).Where("id=? AND state IN ('published','ready')", id).Update("state", "withdrawn").Error; e != nil {
				return "", e
			}
		case "story":
			if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", id, c.ContentVersion).Update("public_body", nil).Error; e != nil {
				return "", e
			}
		case "application":
			var a entity.Application
			if e = db.Where("id=?", id).Take(&a).Error; e != nil {
				return "", e
			}
			if a.Status != "accepted" {
				if e = wish.CancelApplication(ctx, db, a, "invalidated", "content_violation"); e != nil {
					return "", e
				}
			}
		case "message":
			var m entity.Message
			if e = db.Where("id=?", id).Take(&m).Error; e != nil {
				return "", e
			}
			if m.Status == "pending_scan" || m.Status == "pending_review" {
				if e = chat.CancelMessage(ctx, db, m); e != nil {
					return "", e
				}
			}
		}
	}
	switch c.Kind {
	case "wish", "story":
		e = db.Model(&entity.Wish{}).Where("id=?", id).Updates(map[string]any{"version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1")}).Error
	case "application":
		e = db.Model(&entity.Application{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error
	case "message":
		e = db.Model(&entity.Message{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error
	}
	if e != nil {
		return "", e
	}
	return id, audit(db, tx.Actor().ID, c.ID, "content."+op)
}
func restrictionSummary(db *gorm.DB, c entity.ContentRegistry) (Summary, error) {
	out := Summary{ID: c.EntityID, Kind: c.Kind, ContentVersion: c.ContentVersion, Status: "released"}
	var n int64
	if e := db.Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind=? AND c.entity_id=? AND r.active", c.Kind, c.EntityID).Count(&n).Error; e != nil {
		return out, e
	}
	if n > 0 {
		out.Status = "restricted"
	}
	var model any
	switch c.Kind {
	case "wish", "story":
		model = &entity.Wish{}
	case "application":
		model = &entity.Application{}
	case "message":
		model = &entity.Message{}
	}
	e := db.Model(model).Select("version").Where("id=?", c.EntityID).Scan(&out.Version).Error
	return out, e
}
