package resourceplaza

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ReadInteractionContent 在调用方已锁主体、分类和资料的事务内再次执行资料查看资格。
func (s *Service) ReadInteractionContent(ctx context.Context, db *gorm.DB, now time.Time, actorID, id string) (entity.ResourceMaterial, error) {
	if actorID == "" {
		return entity.ResourceMaterial{}, command.ErrUnauthenticated
	}
	var n int64
	if e := db.WithContext(ctx).Model(&entity.Account{}).Where("id=? AND lifecycle='active' AND verification='verified'", actorID).Count(&n).Error; e != nil {
		return entity.ResourceMaterial{}, e
	}
	if n != 1 {
		return entity.ResourceMaterial{}, command.ErrVerificationRequired
	}
	if e := db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind='ban' AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", actorID, now, now).Count(&n).Error; e != nil {
		return entity.ResourceMaterial{}, e
	}
	if n > 0 {
		return entity.ResourceMaterial{}, command.ErrForbidden
	}
	row, e := material(db, id, actor{ID: actorID, Now: now})
	if e != nil {
		return row, e
	}
	cat, e := category(db, row.CategoryID)
	if e != nil {
		return row, e
	}
	if !cat.Enabled {
		return row, command.ErrNotFound
	}
	return row, nil
}
