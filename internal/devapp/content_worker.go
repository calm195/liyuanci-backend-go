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

// ContentWorkerConfig 显式选择隔离分析模式和总预算，缺省不提供放行能力。
// 消息配置为空时仅补偿数据库，数据库和供应商不归调度器所有。
type ContentWorkerConfig struct {
	Analyzer string
	MQ       *workrunner.Config
}

type developmentAnalyzer struct {
	logger    *zap.Logger
	synthetic bool
}

func (a developmentAnalyzer) Analyze(ctx context.Context, _ wish.Content) (out moderation.Assessment, err error) {
	defer completed(a.logger, "development_content_analysis", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if !a.synthetic {
		return out, command.ErrUnavailable
	}
	// 合成模式清除所有原始正文与标签；只验证技术链路，不模拟隐私识别能力。
	a.logger.Info("开发内容分析使用合成结果", zap.Bool("simulated", true), zap.String("result", "success"))
	return moderation.Assessment{Body: "[开发合成审核正文]", DetectorVersion: "development-synthetic-v1"}, nil
}

// AnalyzeMessage 在显式合成模式只产生分类结果，保留私聊主动发送的联系方式。
func (a developmentAnalyzer) AnalyzeMessage(ctx context.Context, _ string) (out moderation.MessageAssessment, err error) {
	defer completed(a.logger, "development_message_analysis", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if !a.synthetic {
		return out, command.ErrUnavailable
	}
	return moderation.MessageAssessment{DetectorVersion: "development-synthetic-private-v1"}, nil
}

// ValidateContentWorker 在任何数据库或消息客户端启动前拒绝生产环境、未知分析模式及跨领域事件。
func ValidateContentWorker(environment string, cfg Settings, options ContentWorkerConfig) error {
	if environment != "integration" || cfg.WorkerDSN == "" || len(cfg.WrapKey) != 32 || len(cfg.LookupKey) != 32 ||
		(options.Analyzer != "" && options.Analyzer != "unavailable" && options.Analyzer != "synthetic") {
		return command.ErrInvalid
	}
	if options.MQ != nil && (workrunner.Validate(*options.MQ, wish.ReviewRoute()) != nil || options.MQ.OperationTimeout < 20*time.Second || options.MQ.MQ.HandlerTimeout < 20*time.Second) {
		return command.ErrInvalid
	}
	return nil
}

// RunContentWorker 装配心愿内容常驻进程；默认无供应商失败关闭，只有显式 synthetic 才使用合成结果。
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
	db, closePool, err := pool(startup, cfg.WorkerDSN, "liyuanci_domain_worker")
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
	scanner, err := moderation.NewWorker(db, logger, codec, developmentAnalyzer{logger: logger, synthetic: options.Analyzer == "synthetic"}, moderation.WorkerConfig{
		Consumer:        durable.ConsumerConfig{Timeout: 5 * time.Second, LockTimeout: time.Second, RetryDelay: time.Minute, MaxAttempts: 3},
		AnalysisTimeout: 5 * time.Second,
		MessageAnalyzer: developmentAnalyzer{logger: logger, synthetic: options.Analyzer == "synthetic"},
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
	logger.Info("开发内容工作进程启动完成", zap.Bool("simulated", options.Analyzer == "synthetic"), zap.Bool("mq_enabled", options.MQ != nil), zap.String("result", "success"))
	<-ctx.Done()
	return nil
}
