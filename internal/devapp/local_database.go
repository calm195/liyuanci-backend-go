package devapp

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// pool 复用顶层数据库连接及预算，并拒绝拥有集群管理权限的账号。
func (cfg Settings) pool(ctx context.Context, log *zap.Logger) (*gorm.DB, func() error, error) {
	return platform.OpenRuntimeDatabaseWithLimits(ctx, cfg.DatabaseDSN, log, cfg.DatabaseMaxOpen, cfg.DatabaseMaxIdle, cfg.DatabaseMaxLifetime)
}
