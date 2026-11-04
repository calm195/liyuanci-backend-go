package testconfig

import (
	"context"
	"embed"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

//go:embed sql/restricted_roles.sql
var roleFiles embed.FS

// ApplyDatabaseRoles 装配隔离集成测试的受限角色，用于额外验证最小权限，不参与应用运行。
// 不分发登录身份或凭据；现有不安全组属性将拒绝执行而非静默降低其他账号权限。
func ApplyDatabaseRoles(ctx context.Context, db *gorm.DB, logger *zap.Logger) (err error) {
	if db == nil || logger == nil {
		return errors.New("必须注入数据库和日志器")
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("数据库权限配置完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	sql, err := roleFiles.ReadFile("sql/restricted_roles.sql")
	if err != nil {
		return errors.New("权限文件读取失败")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 与迁移串行，防止授权矩阵只覆盖一半表结构。
		if err := tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109173)").Error; err != nil {
			return errors.New("权限配置锁失败")
		}
		if err := tx.Exec(string(sql)).Error; err != nil {
			return errors.New("权限配置失败")
		}
		return nil
	})
}
