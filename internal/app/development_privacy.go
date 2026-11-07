package app

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

func developmentPrivacyConfig(cfg DevelopmentSettings) privacy.Config {
	return privacy.Config{
		Command:    developmentCommandConfig(),
		Key:        privacy.DeriveKey(cfg.WishKey),
		ReceiptTTL: 7 * 24 * time.Hour,
		RetryDelay: time.Minute,
	}
}

func openDevelopmentJournal(ctx context.Context, cfg DevelopmentSettings, logger *zap.Logger) (out recoveryJournal, closeAll func(), err error) {
	codec, e := securedata.New(local.Keys{Key: cfg.RecoveryWrapKey, Logger: logger}, logger)
	if e != nil {
		return nil, nil, e
	}
	journal, e := privacy.NewFileJournal(cfg.JournalFile, codec, logger)
	return journal, func() {}, e
}

// RunPrivacyWorker 用于开发数据库清理；使用本机脱敏策略，不提供外部副本删除。
func RunPrivacyWorker(ctx context.Context, environment string, cfg DevelopmentSettings, scope string, logger *zap.Logger, check, recoverOnly bool) (err error) {
	if logger == nil {
		return command.ErrInvalid
	}
	defer finishDevelopment(logger, "privacy_worker", time.Now(), &err)
	if (environment != "integration" && environment != "development") || (scope != "account" && scope != "evidence") {
		return command.ErrInvalid
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	journal, closeJournal, e := openDevelopmentJournal(startup, cfg, logger)
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
	defer func() {

		e := closePool()
		finishDevelopment(logger, "privacy_worker_pool_close", time.Now(), &e)
	}()
	codec, e := securedata.New(&local.IdentityKeys{WrapKey: cfg.WrapKey, LookupKey: cfg.LookupKey, Logger: logger}, logger)
	if e != nil {
		return e
	}
	policy := local.WhisperProvider{Logger: logger}
	pcfg := developmentPrivacyConfig(cfg)
	if scope == "account" && cfg.Tencent.Enabled {
		objects, failure := openObjectGateway(RuntimeSettings{Tencent: cfg.Tencent, ProviderTimeout: cfg.ProviderTimeout}, studentauth.AvatarMaxBytes, logger)
		if failure != nil {
			return failure
		}
		defer objects.Close()
		pcfg.AvatarObjects = objects
	}
	pcfg.Redactor = policy
	s, e := privacy.New(db, journal, codec, logger, pcfg)
	if e != nil {
		return e
	}
	if check {
		return nil
	}
	// 恢复模式先重放账本，再排空当前域；未完成任务不得报告恢复成功。
	if recoverOnly {
		bounded, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		if err := replayDeletionIntents(bounded, s, scope); err != nil {
			return err
		}
		if err := drainDeletionTasks(bounded, s, db, scope); err != nil {
			return err
		}
		return checkDeletionRecovery(bounded, db, journal, scope)
	}
	logger.Info(
		"开发隐私工作进程启动完成",
		zap.String("scope", scope),
		zap.Bool("external_cleanup_configured", false),
		zap.String("result", "success"),
	)
	return s.Run(ctx, scope, time.Second)
}
