// Package migration 通过版本 SQL 管理数据库结构、受控回退与显式权限配置。
package migration

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

//go:embed sql/*.sql metadata/*.sql
var files embed.FS

// Run 仅执行版本 SQL 和结构校验，供隔离结构测试使用；应用命令调用 RunApplication。
func Run(ctx context.Context, db *gorm.DB, logger *zap.Logger, direction string) error {
	return run(ctx, db, logger, direction, false)
}

// RunApplication 在同一事务中完成版本 SQL、结构校验和当前应用权限配置。
// 权限阶段失败时，结构及版本账本一起回滚；普通进程不能调用此显式 DDL 入口。
func RunApplication(ctx context.Context, db *gorm.DB, logger *zap.Logger, direction string) error {
	return run(ctx, db, logger, direction, true)
}

func run(ctx context.Context, db *gorm.DB, logger *zap.Logger, direction string, application bool) (err error) {
	if db == nil || logger == nil {
		return errors.New("必须注入数据库和日志器")
	}
	start := time.Now()
	stage, version := "catalog", ""
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info(
			"数据库迁移完成",
			zap.String("direction", direction),
			zap.String("stage", stage),
			zap.String("version", version),
			zap.String("result", result),
			zap.Duration("duration", time.Since(start)),
		)
	}()
	if direction != "up" && direction != "down" {
		return errors.New("迁移方向必须为 up 或 down")
	}
	entries, err := loadVersions()
	if err != nil {
		return err
	}
	ledgerSQL, err := files.ReadFile("metadata/ledger.sql")
	if err != nil {
		return errors.New("迁移账本定义读取失败")
	}
	stage = "ledger"
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 所有版本共享固定技术锁，不依赖 Redis 或进程内互斥。
		if e := tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109173)").Error; e != nil {
			return fmt.Errorf("迁移锁失败：%w", e)
		}
		var recovery bool
		if tx.Raw("SELECT to_regclass('public.recovery_schema_migrations') IS NOT NULL OR to_regclass('public.recovery_delete_journal') IS NOT NULL").
			Scan(&recovery).
			Error != nil ||
			recovery {
			return errors.New("业务迁移不能使用恢复日志数据库")
		}
		// 显式限定 schema，避免连接 search_path 将账本或账号表放到错误命名空间。
		if e := tx.Exec(string(ledgerSQL)).Error; e != nil {
			return fmt.Errorf("迁移账本初始化失败：%w", e)
		}
		var rows []struct {
			Version  int64
			Checksum string
		}
		if e := tx.Model(&entity.SchemaMigration{}).Select("version, checksum").Order("version").Find(&rows).Error; e != nil {
			return fmt.Errorf("迁移账本读取失败：%w", e)
		}
		// 账本必须是制品目录的完整前缀，拒绝编号缺口及校验和不符的旧原型结构。
		if len(rows) > len(entries) {
			return errors.New("迁移版本超出当前制品")
		}
		for i, row := range rows {
			if row.Version != entries[i].version || row.Checksum != entries[i].checksum {
				return errors.New("迁移版本或校验和不匹配")
			}
		}
		if direction == "up" {
			for i := len(rows); i < len(entries); i++ {
				stage, version = "up", entries[i].name
				if e := tx.Exec(string(entries[i].up)).Error; e != nil {
					return fmt.Errorf("应用迁移 %s 失败：%w", entries[i].name, e)
				}
				if e := tx.Create(&entity.SchemaMigration{Version: entries[i].version, Checksum: entries[i].checksum}).Error; e != nil {
					return e
				}
			}
			stage = "schema"
			if e := validateModelColumns(tx, businessModels()); e != nil {
				return e
			}
			if application {
				stage = "permissions"
				return applyApplicationPermissions(tx)
			}
			return nil
		}
		// down 只提供整体空库回退。先锁定全部已注册业务表并检查数据，
		// 避免历史脚本先删表、后续检查遗漏独立业务数据；检查与删除共享事务。
		stage = "rollback_guard"
		if e := guardRollback(tx); e != nil {
			return e
		}
		for i := len(rows) - 1; i >= 0; i-- {
			stage, version = "down", entries[i].name
			if e := rollbackVersion(tx, entries[i]); e != nil {
				return fmt.Errorf("回退迁移 %s 失败：%w", entries[i].name, e)
			}
		}
		return nil
	})
}

// rollbackVersion 仅供持有事务的受控回退路径使用，不扩大整体空库回退的公开范围。
func rollbackVersion(tx *gorm.DB, entry versionEntry) error {
	if err := tx.Exec(string(entry.down)).Error; err != nil {
		return err
	}
	return tx.Where("version = ?", entry.version).Delete(&entity.SchemaMigration{}).Error
}
