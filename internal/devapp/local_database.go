package devapp

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// pool 根据显式配置选择共用测试身份，正式分角色连接校验保持原边界。
func (cfg Settings) pool(ctx context.Context, dsn, role string, log *zap.Logger) (*gorm.DB, func() error, error) {
	if cfg.LocalTest {
		return platform.OpenLocalDatabase(ctx, "integration", cfg.DatabaseDSN, log)
	}
	return pool(ctx, dsn, role)
}
