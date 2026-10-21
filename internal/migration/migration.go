// Package migration 在受控迁移入口协调实体自动迁移与 PostgreSQL 专用约束。
package migration

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

//go:embed sql/*.sql
var files embed.FS

// Run 按顺序应用全部待执行版本或逆序回退全部版本；整个批次在单个事务中完成。
func Run(ctx context.Context, db *gorm.DB, logger *zap.Logger, direction string) (err error) {
	if db == nil || logger == nil {
		return errors.New("必须注入数据库和日志器")
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("数据库迁移完成", zap.String("direction", direction), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if direction != "up" && direction != "down" {
		return errors.New("迁移方向必须为 up 或 down")
	}
	catalog := []string{"0001_accounts", "0002_identity", "0003_identity_admin", "0004_interactions", "0005_moderation_work_evidence", "0006_outbox_supersession", "0007_admin_local", "0008_admin_roles", "0009_student_login", "0010_student_card"}
	type entry struct {
		up, down []byte
		checksum string
	}
	entries := make([]entry, len(catalog))
	for i, name := range catalog {
		up, e := files.ReadFile("sql/" + name + ".up.sql")
		if e != nil {
			return errors.New("迁移文件读取失败")
		}
		down, e := files.ReadFile("sql/" + name + ".down.sql")
		if e != nil {
			return errors.New("回退文件读取失败")
		}
		sum := sha256.Sum256(append(append([]byte(nil), up...), down...))
		entries[i] = entry{up: up, down: down, checksum: hex.EncodeToString(sum[:])}
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 所有版本共享固定技术锁，不依赖 Redis 或进程内互斥。
		if e := tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109173)").Error; e != nil {
			return fmt.Errorf("迁移锁失败：%w", e)
		}
		var recovery bool
		if tx.Raw("SELECT to_regclass('public.recovery_schema_migrations') IS NOT NULL OR to_regclass('public.recovery_delete_journal') IS NOT NULL").Scan(&recovery).Error != nil || recovery {
			return errors.New("业务迁移不能使用恢复日志数据库")
		}
		// 显式限定 schema，避免连接 search_path 将账本或账号表放到错误命名空间。
		if e := tx.Exec(`CREATE TABLE IF NOT EXISTS public.schema_migrations (version bigint PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP);
COMMENT ON TABLE public.schema_migrations IS '显式数据库迁移版本及文件完整性账本';
COMMENT ON COLUMN public.schema_migrations.version IS '已成功应用的迁移版本';
COMMENT ON COLUMN public.schema_migrations.checksum IS '对应上下行迁移文件的 SHA-256 校验和';
COMMENT ON COLUMN public.schema_migrations.applied_at IS '迁移事务应用时间';`).Error; e != nil {
			return fmt.Errorf("迁移账本初始化失败：%w", e)
		}
		var rows []struct {
			Version  int64
			Checksum string
		}
		if e := tx.Model(&entity.SchemaMigration{}).Select("version, checksum").Order("version").Find(&rows).Error; e != nil {
			return fmt.Errorf("迁移账本读取失败：%w", e)
		}
		// 账本必须是已知版本的连续前缀，拒绝缺口、未知版本及任何历史文件改写。
		if len(rows) > len(entries) {
			return errors.New("迁移版本超出当前制品")
		}
		for i, row := range rows {
			if row.Version != int64(i+1) || row.Checksum != entries[i].checksum {
				return errors.New("迁移版本或校验和不匹配")
			}
		}
		if direction == "up" {
			for i := len(rows); i < len(entries); i++ {
				if e := tx.Exec(string(entries[i].up)).Error; e != nil {
					return fmt.Errorf("应用迁移失败：%w", e)
				}
				if e := tx.Create(&entity.SchemaMigration{Version: int64(i + 1), Checksum: entries[i].checksum}).Error; e != nil {
					return e
				}
			}
			// 历史迁移先建立生成列、隔离域、权限和锁函数；实体仅同步常规字段。
			// 与版本迁移共享事务和锁，任一失败会回滚，运行账号无需 DDL 权限。
			if e := tx.AutoMigrate(entity.BusinessModels()...); e != nil {
				return fmt.Errorf("实体自动迁移失败：%w", e)
			}
			return nil
		}
		for i := len(rows) - 1; i >= 0; i-- {
			if e := tx.Exec(string(entries[i].down)).Error; e != nil {
				return fmt.Errorf("回退迁移失败：%w", e)
			}
			if e := tx.Where("version = ?", i+1).Delete(&entity.SchemaMigration{}).Error; e != nil {
				return e
			}
		}
		return nil
	})
}

// ApplyPermissions 为专用业务库初始化无登录权限组与当前表授权；只供管理员显式调用。
// 不分发登录身份或凭据；现有不安全组属性将拒绝执行而非静默降低其他账号权限。
func ApplyPermissions(ctx context.Context, db *gorm.DB, logger *zap.Logger) (err error) {
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
	sql, err := files.ReadFile("sql/bootstrap_roles.sql")
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
