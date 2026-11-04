package migration

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ApplyApplicationPermissions 显式配置单账号数据库访问，领域函数继续校验业务角色。
// 仅重放表策略和访问权限，不创建或替换函数；结构变更必须执行 up。
func ApplyApplicationPermissions(ctx context.Context, db *gorm.DB, logger *zap.Logger) (err error) {
	if db == nil || logger == nil {
		return errors.New("必须注入数据库和日志器")
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("应用账号权限配置完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109173)").Error != nil {
			return errors.New("应用权限配置锁失败")
		}
		if err := requireCurrentVersion(tx); err != nil {
			return err
		}
		if err := validateModelColumns(tx, businessModels()); err != nil {
			return err
		}
		return applyApplicationPermissions(tx)
	})
}

// applyApplicationPermissions 只在调用方已持有迁移锁的事务内执行。
func applyApplicationPermissions(tx *gorm.DB) error {
	sql, err := files.ReadFile("sql/application_permissions.sql")
	if err != nil {
		return errors.New("应用权限模板读取失败")
	}
	if tx.Exec(string(sql)).Error != nil {
		return errors.New("应用权限配置失败，请检查账号属性和业务库对象所有权")
	}
	return nil
}
