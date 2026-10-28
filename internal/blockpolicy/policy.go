// Package blockpolicy 提供独立于业务模块开关的匿名黑名单单向策略。
package blockpolicy

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// Enabled 读取数据库权威策略；缺行或存储失败拒绝降级旧接口。
func Enabled(ctx context.Context, db *gorm.DB) (bool, error) {
	var rows []struct{ OpaqueEnabled bool }
	e := db.WithContext(ctx).Raw("SELECT opaque_enabled FROM public.interaction_block_policy WHERE singleton").Scan(&rows).Error
	if e != nil || len(rows) != 1 {
		return false, command.ErrUnavailable
	}
	return rows[0].OpaqueEnabled, nil
}

// Legacy 在认证后复核旧边界，防止旧回执绕过切换。
func Legacy(ctx context.Context, db *gorm.DB) error {
	enabled, e := Enabled(ctx, db)
	if e != nil {
		return e
	}
	if enabled {
		return command.ErrClientUpgradeRequired
	}
	return nil
}

// Enable 只供显式运维入口使用迁移身份执行；同事务排空旧命令并验证历史随机标识。
func Enable(ctx context.Context, db *gorm.DB, log *zap.Logger) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		log.Info("匿名黑名单切换完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SELECT pg_advisory_xact_lock(624109175)").Error; e != nil {
			return e
		}
		var invalid int64
		if e := tx.Raw("SELECT count(*) FROM public.blocks WHERE block_id IS NULL OR version<1").Scan(&invalid).Error; e != nil {
			return e
		}
		if invalid != 0 {
			return command.ErrConflict
		}
		result := tx.Exec("UPDATE public.interaction_block_policy SET opaque_enabled=true,switched_at=COALESCE(switched_at,clock_timestamp()) WHERE singleton")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return command.ErrUnavailable
		}
		return nil
	})
}
