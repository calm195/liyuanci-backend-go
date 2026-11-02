package resourceplaza

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// SetBarrier 是受控治理装配调用的内部入口，不注册任何学生举报或普通后台HTTP路由。
// 调用方必须持有治理服务能力；与编辑使用相同分类、资料锁，屏障改变使资料离线且增加资源版本。
func (s *Service) SetBarrier(ctx context.Context, token, rid, key, id string, privacy, content bool) (err error) {
	defer s.done("barrier", rid, time.Now(), &err)
	return s.transaction(ctx, token, true, true, func(db *gorm.DB, a actor) error {
		// 受控操作只允许超级管理员，普通review_operator没有此能力。
		var n int64
		if e := db.Model(&entity.AdminRoleBinding{}).Where("admin_id=? AND role='super_admin'", a.ID).Count(&n).Error; e != nil {
			return e
		}
		if n != 1 {
			return command.ErrForbidden
		}
		var r entity.ResourceMaterial
		if e := db.Where("id=?", id).Take(&r).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrNotFound
			}
			return e
		}
		oldCategory := r.CategoryID
		if _, e := command.LockPlan(ctx, db, command.Discovery{Resources: []command.Resource{{Kind: command.ResourceCategory, ID: r.CategoryID}, {Kind: command.ResourceMaterial, ID: id}}}, s.cfg.Command.LockTimeout); e != nil {
			return e
		}
		if e := db.Where("id=?", id).Take(&r).Error; e != nil {
			return e
		}
		if oldCategory != r.CategoryID {
			return command.ErrRetry
		}
		if r.PrivacyBlocked == privacy && r.ContentBlocked == content {
			return nil
		}
		if r.Version == MaxInteger {
			return command.ErrUnavailable
		}
		r.Version++
		r.PrivacyBlocked = privacy
		r.ContentBlocked = content
		if r.Status == "published" {
			r.Status = "offline"
		}
		r.UpdatedAt = a.Now
		if e := db.Save(&r).Error; e != nil {
			return e
		}
		return s.audit(db, a, "barrier", id, r.Version, "")
	})
}
