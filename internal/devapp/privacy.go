package devapp

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

func privacyConfig(cfg Settings) privacy.Config {
	return privacy.Config{Command: commandConfig(), Key: privacy.DeriveKey(cfg.WishKey), ReceiptTTL: 7 * 24 * time.Hour, RetryDelay: time.Minute}
}

func openDeletionJournal(ctx context.Context, cfg Settings, logger *zap.Logger) (out privacy.Journal, closeAll func(), err error) {
	codec, e := securedata.New(local.Keys{Key: cfg.RecoveryWrapKey, Logger: logger}, logger)
	if e != nil {
		return nil, nil, e
	}
	journal, e := privacy.NewFileJournal(cfg.JournalFile, codec, logger)
	return journal, func() {}, e
}

// RunPrivacyWorker 只用于显式隔离开发环境；没有正式清理/脱敏适配器时保持待办，不模拟完成。
// 生产宿主须显式注入privacy.Eraser/Redactor后调用Service.Run，不使用本地密钥适配器。
func RunPrivacyWorker(ctx context.Context, environment string, cfg Settings, scope string, logger *zap.Logger) (err error) {
	if logger == nil {
		return command.ErrInvalid
	}
	defer completed(logger, "privacy_worker", time.Now(), &err)
	if (environment != "integration" && environment != "development") || (scope != "account" && scope != "evidence") {
		return command.ErrInvalid
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	journal, closeJournal, e := openDeletionJournal(startup, cfg, logger)
	if e != nil {
		return e
	}
	defer closeJournal()
	var db *gorm.DB
	var closePool func() error
	db, closePool, e = cfg.pool(startup, logger)
	if e != nil {
		return e
	}
	defer func() { e := closePool(); completed(logger, "privacy_worker_pool_close", time.Now(), &e) }()
	codec, e := securedata.New(&local.IdentityKeys{WrapKey: cfg.WrapKey, LookupKey: cfg.LookupKey, Logger: logger}, logger)
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
