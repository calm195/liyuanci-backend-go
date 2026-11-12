package wish

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ResolveBlockSource 在调用者已持有账号与心愿锁的事务内，复核精确公开来源后解析屏蔽目标。
// 真实账号仅供领域协作使用，不得直接回传客户端。
func (s *Service) ResolveBlockSource(ctx context.Context, db *gorm.DB, now time.Time, viewer, kind, id string, contentVersion int64) (string, error) {
	if !validWishID(id) || contentVersion < 1 || (kind != "wish" && kind != "story") {
		return "", command.ErrInvalid
	}
	projected, err := s.shareTarget(ctx, db, kind, id, "", now)
	if err != nil {
		return "", err
	}
	if projected.ContentVersion != contentVersion {
		return "", command.ErrNotFound
	}
	a, err := readAggregate(ctx, db, id, false)
	if err != nil {
		return "", err
	}
	if viewer == a.row.OwnerID {
		return "", command.ErrInvalid
	}
	return a.row.OwnerID, nil
}
