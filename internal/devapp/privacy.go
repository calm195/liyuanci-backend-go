package devapp

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/localtest"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

func privacyConfig(cfg Settings) privacy.Config {
	return privacy.Config{Command: commandConfig(), Key: privacy.DeriveKey(cfg.WishKey), ReceiptTTL: 7 * 24 * time.Hour, RetryDelay: time.Minute}
}

func openDeletionJournal(ctx context.Context, cfg Settings, logger *zap.Logger) (out privacy.Journal, closeAll func(), err error) {
	if cfg.LocalTest {
		codec, e := securedata.New(localtest.Keys{Key: cfg.RecoveryWrapKey, Logger: logger}, logger)
		if e != nil {
			return nil, nil, e
		}
		journal, e := localtest.NewJournal(cfg.JournalFile, codec, logger)
		return journal, func() {}, e
	}
	if cfg.RecoveryWriterDSN == "" || cfg.RecoveryReaderDSN == "" || len(cfg.RecoveryWrapKey) != 32 {
		return nil, nil, command.ErrInvalid
	}
	var closes []func() error
	closeAll = func() {
		for i := len(closes) - 1; i >= 0; i-- {
			e := closes[i]()
			completed(logger, "privacy_journal_pool_close", time.Now(), &e)
		}
	}
	defer func() {
		if err != nil {
			closeAll()
		}
	}()
	writer, c, e := pool(ctx, cfg.RecoveryWriterDSN, "liyuanci_recovery_writer")
	if e != nil {
		return nil, closeAll, e
	}
	closes = append(closes, c)
	reader, c, e := pool(ctx, cfg.RecoveryReaderDSN, "liyuanci_recovery_reader")
	if e != nil {
		return nil, closeAll, e
	}
	closes = append(closes, c)
	codec, e := securedata.New(&localKeys{wrap: cfg.RecoveryWrapKey, logger: logger}, logger)
	if e != nil {
		return nil, closeAll, e
	}
	out, e = privacy.NewJournal(ctx, writer, reader, codec, logger, 5*time.Second)
	return out, closeAll, e
}

// RunPrivacyWorker 只用于显式隔离开发环境；没有正式清理/脱敏适配器时保持待办，不模拟完成。
// 生产宿主须显式注入privacy.Eraser/Redactor后调用Service.Run，不使用本地密钥适配器。
func RunPrivacyWorker(ctx context.Context, environment string, cfg Settings, scope string, logger *zap.Logger) (err error) {
	if logger == nil {
		return command.ErrInvalid
	}
	defer completed(logger, "privacy_worker", time.Now(), &err)
	if environment != "integration" || (scope != "account" && scope != "evidence") {
		return command.ErrInvalid
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	journal, closeJournal, e := openDeletionJournal(startup, cfg, logger)
	if e != nil {
		return e
	}
	defer closeJournal()
	dsn, role := cfg.PrivacyDSN, "liyuanci_privacy_maintenance"
	if scope == "evidence" {
		dsn, role = cfg.EvidencePurgerDSN, "liyuanci_evidence_purger"
	}
	var db *gorm.DB
	var closePool func() error
	db, closePool, e = cfg.pool(startup, dsn, role, logger)
	if e != nil {
		return e
	}
	defer func() { e := closePool(); completed(logger, "privacy_worker_pool_close", time.Now(), &e) }()
	codec, e := securedata.New(&localKeys{wrap: cfg.WrapKey, lookup: cfg.LookupKey, logger: logger}, logger)
	if e != nil {
		return e
	}
	s, e := privacy.New(db, journal, codec, nil, logger, privacyConfig(cfg))
	if e != nil {
		return e
	}
	logger.Info("开发隐私工作进程启动完成", zap.String("scope", scope), zap.Bool("external_cleanup_configured", false), zap.String("result", "success"))
	return s.Run(ctx, scope, time.Second)
}
