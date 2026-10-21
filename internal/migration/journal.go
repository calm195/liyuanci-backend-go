package migration

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

//go:embed journal/*.sql
var journalFiles embed.FS

// RunJournal 在独立数据库显式部署恢复意图日志，不表示独立存储和备份恢复已验收。
func RunJournal(ctx context.Context, db *gorm.DB, logger *zap.Logger, action string) (err error) {
	if db == nil || logger == nil {
		return errors.New("必须注入数据库和日志器")
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("恢复日志结构操作完成", zap.String("operation", action), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if action != "up" && action != "down" && action != "permissions" {
		return errors.New("恢复日志操作无效")
	}
	up, err := journalFiles.ReadFile("journal/0001_journal.up.sql")
	if err != nil {
		return errors.New("恢复日志迁移文件读取失败")
	}
	down, err := journalFiles.ReadFile("journal/0001_journal.down.sql")
	if err != nil {
		return errors.New("恢复日志回退文件读取失败")
	}
	permissions, err := journalFiles.ReadFile("journal/permissions.sql")
	if err != nil {
		return errors.New("恢复日志权限文件读取失败")
	}
	sum := sha256.Sum256(append(append([]byte(nil), up...), down...))
	checksum := hex.EncodeToString(sum[:])
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 与业务迁移使用同一数据库内锁，阻止空库并发初始化成两种角色。
		if tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109173)").Error != nil {
			return errors.New("恢复日志结构锁失败")
		}
		var business bool
		if tx.Raw("SELECT to_regclass('public.accounts') IS NOT NULL OR to_regclass('public.schema_migrations') IS NOT NULL").Scan(&business).Error != nil || business {
			return errors.New("恢复日志必须使用独立数据库")
		}
		if tx.Exec(`CREATE TABLE IF NOT EXISTS public.recovery_schema_migrations (version bigint PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP);
COMMENT ON TABLE public.recovery_schema_migrations IS '独立恢复日志结构版本账本';
COMMENT ON COLUMN public.recovery_schema_migrations.version IS '恢复日志结构已应用版本';
COMMENT ON COLUMN public.recovery_schema_migrations.checksum IS '恢复日志上下行文件 SHA-256 校验和';
COMMENT ON COLUMN public.recovery_schema_migrations.applied_at IS '恢复日志结构版本应用时间';`).Error != nil {
			return errors.New("恢复日志账本初始化失败")
		}
		var rows []struct {
			Version  int64
			Checksum string
		}
		if tx.Raw("SELECT version, checksum FROM public.recovery_schema_migrations ORDER BY version").Scan(&rows).Error != nil {
			return errors.New("恢复日志账本读取失败")
		}
		if len(rows) > 1 || (len(rows) == 1 && (rows[0].Version != 1 || rows[0].Checksum != checksum)) {
			return errors.New("恢复日志账本不匹配")
		}
		switch action {
		case "up":
			if len(rows) == 1 {
				return nil
			}
			if tx.Exec(string(up)).Error != nil {
				return errors.New("恢复日志迁移失败")
			}
			return tx.Exec("INSERT INTO public.recovery_schema_migrations(version, checksum) VALUES (1, ?)", checksum).Error
		case "down":
			if len(rows) == 0 {
				return nil
			}
			if tx.Exec(string(down)).Error != nil {
				return errors.New("恢复日志非空或回退失败")
			}
			return tx.Exec("DELETE FROM public.recovery_schema_migrations WHERE version = 1").Error
		default:
			if len(rows) != 1 {
				return errors.New("恢复日志权限配置需要已应用结构")
			}
			if tx.Exec(string(permissions)).Error != nil {
				return errors.New("恢复日志权限配置失败")
			}
			return nil
		}
	})
}
