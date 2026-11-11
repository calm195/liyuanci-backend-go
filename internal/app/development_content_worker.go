package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/execution/workrunner"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// ContentWorkerConfig 控制开发内容进程的检查行为；原文复核策略固定由词库与人工审核决定。
// 工作持久化在数据库，数据库和供应商不归调度器所有。
type ContentWorkerConfig struct {
	// Check 只装配依赖，不启动任务消费者。
	Check bool
}

// ValidateContentWorker 在数据库启动前拒绝生产环境与缺失的身份依赖。
func ValidateContentWorker(environment string, cfg DevelopmentSettings) error {
	if (environment != "integration" && environment != "development") || cfg.DatabaseDSN == "" || len(cfg.WrapKey) != 32 ||
		len(cfg.LookupKey) != 32 {
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
	if err = ValidateContentWorker(environment, cfg); err != nil {
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
	// 字典和关怀资料统一从版本化资源文件读取，不再接受内联或单个联系方式。
	resources, err := loadResources(ctx, RuntimeSettings{LocalTest: cfg.LocalTest, ResourcesFile: cfg.ResourcesFile})
	if err != nil {
		return err
	}
	analyzer := local.Analyzer{Logger: logger}
	scanner, err := moderation.NewWorker(db, logger, codec, analyzer, moderation.WorkerConfig{
		Care: resources.Care,
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
	if options.Check {
		return nil
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
	auto, e := matchmaking.NewAutoWorker(db, logger, codec, durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Second, MaxAttempts: 3})
	if e != nil {
		return e
	}
	autoRunner, e := workrunner.OpenDatabase(ctx, auto, logger, time.Second, 30*time.Second)
	if e != nil {
		return e
	}
	defer autoRunner.Close()
	logger.Info(
		"开发内容工作进程启动完成",
		zap.Bool("manual_review", true),
		zap.String("result", "success"),
	)
	<-ctx.Done()
	return nil
}
