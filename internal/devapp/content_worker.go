package devapp

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

// ContentWorkerConfig 显式选择隔离分析模式和总预算，缺省不提供放行能力，manual强制人工复核。
// 消息配置为空时仅补偿数据库，数据库和供应商不归调度器所有。
type ContentWorkerConfig struct {
	Analyzer string
	MQ       *workrunner.Config
}

type developmentAnalyzer struct {
	logger    *zap.Logger
	synthetic bool
	manual    bool
}

func (a developmentAnalyzer) Analyze(ctx context.Context, input wish.Content) (out moderation.Assessment, err error) {
	defer completed(a.logger, "development_content_analysis", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	// 人工模式保留原文和原标签，只生成必须复核的候选，绝不自动批准。
	if a.manual {
		a.logger.Info("开发内容提交人工复核", zap.Bool("manual_review", true), zap.String("result", "success"))
		return moderation.Assessment{Body: input.Body, Tags: input.IdentityTags, OrdinaryHit: true, DetectorVersion: "development-manual-v1"}, nil
	}
	if !a.synthetic {
		return out, command.ErrUnavailable
	}
	// 合成模式清除所有原始正文与标签；只验证技术链路，不模拟隐私识别能力。
	a.logger.Info("开发内容分析使用合成结果", zap.Bool("simulated", true), zap.String("result", "success"))
	return moderation.Assessment{Body: "[开发合成审核正文]", DetectorVersion: "development-synthetic-v1"}, nil
}

// AnalyzeMessage 保留私聊原文；合成模式只产生分类，人工模式强制交人工复核。
func (a developmentAnalyzer) AnalyzeMessage(ctx context.Context, _ string) (out moderation.MessageAssessment, err error) {
	defer completed(a.logger, "development_message_analysis", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if a.manual {
		return moderation.MessageAssessment{OrdinaryHit: true, DetectorVersion: "development-manual-private-v1"}, nil
	}
	if !a.synthetic {
		return out, command.ErrUnavailable
	}
	return moderation.MessageAssessment{DetectorVersion: "development-synthetic-private-v1"}, nil
}

// ValidateContentWorker 在任何数据库或消息客户端启动前拒绝生产环境、未知分析模式及跨领域事件。
func ValidateContentWorker(environment string, cfg Settings, options ContentWorkerConfig) error {
	if environment != "integration" || cfg.WorkerDSN == "" || len(cfg.WrapKey) != 32 || len(cfg.LookupKey) != 32 ||
		(options.Analyzer != "" && options.Analyzer != "unavailable" && options.Analyzer != "synthetic" && options.Analyzer != "manual") {
		return command.ErrInvalid
	}
	if options.MQ != nil && (workrunner.Validate(*options.MQ, wish.ReviewRoute()) != nil || options.MQ.OperationTimeout < 20*time.Second || options.MQ.MQ.HandlerTimeout < 20*time.Second) {
		return command.ErrInvalid
	}
	return nil
}

// RunContentWorker 装配内容常驻进程；默认无供应商失败关闭，synthetic使用合成结果，manual保留原文并强制人工复核。
// 不发布规则、不迁移、不创建主题；词库缺失与分析故障由领域持久任务重试或转异常待办。
// 取消后先关闭调度和消息消费，再关闭池，保证在途事务不会使用已释放依赖。
func RunContentWorker(ctx context.Context, environment string, cfg Settings, options ContentWorkerConfig, logger *zap.Logger) (err error) {
	if logger == nil {
		return command.ErrInvalid
	}
	defer completed(logger, "content_worker", time.Now(), &err)
	if err = ValidateContentWorker(environment, cfg, options); err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	db, closePool, err := cfg.pool(startup, cfg.WorkerDSN, "liyuanci_domain_worker", logger)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		started := time.Now()
		failure := closePool()
		completed(logger, "content_worker_pool_close", started, &failure)
		if failure != nil {
			err = command.ErrUnavailable
		}
	}()
	codec, err := securedata.New(&localKeys{wrap: cfg.WrapKey, lookup: cfg.LookupKey, logger: logger}, logger)
	if err != nil {
		return err
	}
	analyzer := developmentAnalyzer{logger: logger, synthetic: options.Analyzer == "synthetic", manual: options.Analyzer == "manual"}
	scanner, err := moderation.NewWorker(db, logger, codec, analyzer, moderation.WorkerConfig{
		Care:            cfg.Care,
		Consumer:        durable.ConsumerConfig{Timeout: 5 * time.Second, LockTimeout: time.Second, RetryDelay: time.Minute, MaxAttempts: 3},
		AnalysisTimeout: 5 * time.Second,
		MessageAnalyzer: analyzer,
	})
	if err != nil {
		return err
	}
	var runner *workrunner.Worker
	if options.MQ == nil {
		runner, err = workrunner.OpenDatabase(ctx, scanner, logger, time.Second, 20*time.Second)
	} else {
		if err = transportmq.ConfigureSDKLogging(logger); err != nil {
			return err
		}
		runner, err = workrunner.Open(ctx, db, scanner, logger, *options.MQ, wish.ReviewRoute())
	}
	if err != nil {
		return err
	}
	defer func() {
		if runner.Close() != nil {
			err = command.ErrUnavailable
		}
	}()
	logger.Info("开发内容工作进程启动完成", zap.Bool("simulated", options.Analyzer == "synthetic"), zap.Bool("manual_review", options.Analyzer == "manual"), zap.Bool("mq_enabled", options.MQ != nil), zap.String("result", "success"))
	<-ctx.Done()
	return nil
}
