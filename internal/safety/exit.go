package safety

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// exceptionExit 保持原期限并转为已撤下，不经过普通回退额度检查，也不解除原有内容限制。
func exceptionExit(ctx context.Context, tx *command.Tx, id string) (string, error) {
	db := tx.DB().WithContext(ctx)
	var w entity.Wish
	if e := db.Where("id=?", id).Take(&w).Error; e != nil {
		return "", found(e)
	}
	if w.State != "matching" && w.State != "realized" {
		return "", command.ErrConflict
	}
	var m entity.Match
	if e := db.Where("wish_id=? AND status='current'", id).Take(&m).Error; e != nil {
		return "", found(e)
	}
	actor := tx.Actor().ID
	peer := m.HelperID
	if actor == m.HelperID {
		peer = m.RequesterID
	} else if actor != m.RequesterID {
		return "", command.ErrNotFound
	}
	var n int64
	if e := db.Model(&entity.Block{}).Where("owner_id=? AND target_id=?", actor, peer).Count(&n).Error; e != nil {
		return "", e
	}
	allowed := n > 0
	if e := db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", peer, tx.Now(), tx.Now()).Count(&n).Error; e != nil {
		return "", e
	}
	allowed = allowed || n > 0
	if !allowed {
		return "", command.ErrForbidden
	}
	if e := cancelRelationships(ctx, tx, m.RequesterID, m.HelperID); e != nil {
		return "", e
	}
	if m.ApplicationID != nil {
		var a entity.Application
		if e := db.Where("id=?", *m.ApplicationID).Take(&a).Error; e != nil {
			return "", e
		}
		if e := wish.CancelApplication(ctx, db, a, "invalidated", "exception_exit"); e != nil {
			return "", e
		}
	}
	var apps []entity.Application
	if e := db.Where("wish_id=? AND status IN ('active','pending_scan','pending_review')", id).Find(&apps).Error; e != nil {
		return "", e
	}
	for _, a := range apps {
		if e := wish.CancelApplication(ctx, db, a, "invalidated", "exception_exit"); e != nil {
			return "", e
		}
	}
	if e := db.Model(&entity.Match{}).Where("id=?", m.ID).Updates(map[string]any{"status": "ended", "ended_at": tx.Now(), "version": gorm.Expr("version+1")}).Error; e != nil {
		return "", e
	}
	return id, db.Model(&entity.Wish{}).Where("id=?", id).Updates(map[string]any{"state": "withdrawn", "version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1"), "refresh_on_publish": false}).Error
}
