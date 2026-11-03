package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/execution/workrunner"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// ContentWorkerConfig 选择开发原文复核模式；空值与manual均在词库命中或重审时交人工。
// 工作持久化在数据库，数据库和供应商不归调度器所有。
type ContentWorkerConfig struct {
	Analyzer string
}

// ValidateContentWorker 在数据库启动前拒绝生产环境与未知分析模式。
func ValidateContentWorker(environment string, cfg DevelopmentSettings, options ContentWorkerConfig) error {
	if (environment != "integration" && environment != "development") || cfg.DatabaseDSN == "" || len(cfg.WrapKey) != 32 ||
		len(cfg.LookupKey) != 32 ||
		(options.Analyzer != "" && options.Analyzer != "manual") {
		return command.ErrInvalid
	}
	return nil
}

// RunContentWorker 装配内容常驻进程；按词库筛选，命中/重审保留原文并强制人工复核。
// 不发布规则、不迁移；词库缺失与分析故障由领域持久任务重试或转异常待办。
// 取消后先关闭调度并等待处理结束，再关闭池，保证在途事务不会使用已释放依赖。
func RunContentWorker(ctx context.Context, environment string, cfg DevelopmentSettings, options ContentWorkerConfig, logger *zap.Logger) (err error) {
	if logger == nil {
		return command.ErrInvalid
	}
	defer finishDevelopment(logger, "content_worker", time.Now(), &err)
	if err = ValidateContentWorker(environment, cfg, options); err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	db, closePool, err := cfg.pool(startup, logger)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		started := time.Now()
		failure := closePool()
		finishDevelopment(logger, "content_worker_pool_close", started, &failure)
		if failure != nil {
			err = command.ErrUnavailable
		}
	}()
	codec, err := securedata.New(&local.IdentityKeys{WrapKey: cfg.WrapKey, LookupKey: cfg.LookupKey, Logger: logger}, logger)
	if err != nil {
		return err
	}
	analyzer := local.Analyzer{Logger: logger}
	scanner, err := moderation.NewWorker(db, logger, codec, analyzer, moderation.WorkerConfig{
		Care: careResources(cfg.Care),
		Consumer: durable.ConsumerConfig{
			Timeout:     5 * time.Second,
			LockTimeout: time.Second,
			RetryDelay:  time.Minute,
			MaxAttempts: 3,
		},
		AnalysisTimeout: 5 * time.Second,
		MessageAnalyzer: analyzer,
	})
	if err != nil {
		return err
	}
	runner, err := workrunner.OpenDatabase(ctx, scanner, logger, time.Second, 20*time.Second)
	if err != nil {
		return err
	}
	defer func() {
		if runner.Close() != nil {
			err = command.ErrUnavailable
		}
	}()
	logger.Info(
		"开发内容工作进程启动完成",
		zap.Bool("manual_review", true),
		zap.String("result", "success"),
	)
	<-ctx.Done()
	return nil
}
