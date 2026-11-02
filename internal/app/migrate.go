package app

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

// RunMigration 仅由显式迁移命令调用，拥有本次数据库连接并在返回前关闭。
// 普通API和worker不调用此入口；数据库账号、SQL校验和及权限限制保持原有规则。
func RunMigration(ctx context.Context, path, action string) error {
	log, err := zap.NewProduction()
	if err != nil {
		return errors.New("迁移未完成，请核对配置、操作及依赖")
	}
	defer func() { _ = log.Sync() }()
	start := time.Now()
	result := "failure"
	defer func() {
		log.Info("迁移命令完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if action != "up" && action != "down" && action != "permissions" && action != "opaque-blocks" {
		log.Error("迁移操作无效")
		return errors.New("迁移未完成，请核对配置、操作及依赖")
	}
	settings, err := config.Load(path)
	if err != nil {
		log.Error("应用YAML配置无效")
		return errors.New("迁移未完成，请核对配置、操作及依赖")
	}
	dsn := settings.Database.DSN
	if dsn == "" {
		log.Error("缺少目标数据库外部配置")
		return errors.New("迁移未完成，请核对配置、操作及依赖")
	}
	// 屏蔽驱动 SQL 日志，避免连接错误或未来敏感迁移数据泄漏。
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		log.Error("数据库连接初始化失败")
		return errors.New("迁移未完成，请核对配置、操作及依赖")
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Error("数据库连接获取失败")
		return errors.New("迁移未完成，请核对配置、操作及依赖")
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			log.Error("迁移连接关闭失败")
		} else {
			log.Info("迁移连接关闭完成", zap.String("result", "success"))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if action == "opaque-blocks" {
		err = blockpolicy.Enable(ctx, db, log)
	} else if action == "permissions" {
		err = migration.ApplyApplicationPermissions(ctx, db, log)
	} else {
		err = migration.Run(ctx, db, log, action)
		if err == nil && action == "up" {
			err = migration.ApplyApplicationPermissions(ctx, db, log)
		}
	}
	if err != nil {
		log.Error("迁移执行失败，请核对版本、权限和数据约束")
		return errors.New("迁移未完成，请核对配置、操作及依赖")
	}
	result = "success"
	return nil
}
