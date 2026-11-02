package platform

import (
	"context"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// OpenLocalDatabase 开发模式同样使用唯一应用账号。
func OpenLocalDatabase(ctx context.Context, environment, dsn string, log *zap.Logger) (*gorm.DB, func() error, error) {
	if environment != "integration" {
		return nil, nil, command.ErrInvalid
	}
	return OpenRuntimeDatabase(ctx, dsn, log)
}
