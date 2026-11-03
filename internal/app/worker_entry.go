package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// WorkerOptions 保留各命令的职责选择；Entry由命令固定，不能由YAML切换入口权限。
type WorkerOptions struct {
	ConfigFile, Entry, Kind     string
	Check, CheckConfig, Recover bool
}

// RunWorker 统一加载配置及管理日志，再交给对应宿主装配任务与恢复资源。
// ctx取消后，下层先停止扫描并等待在途事务，返回前关闭自己创建的连接池和供应商。
// 检查模式不消费任务；开发入口继续由领域宿主拒绝生产环境。
func RunWorker(ctx context.Context, options WorkerOptions) (err error) {
	log, err := platform.NewLogger("info")
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()
	start := time.Now()
	kind := options.Kind
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		log.Info(
			"工作进程入口结束",
			zap.String("entry", options.Entry),
			zap.String("kind", kind),
			zap.String("result", result),
			zap.Duration("duration", time.Since(start)),
		)
	}()
	cfg, err := config.Load(options.ConfigFile)
	if err != nil {
		return err
	}
	if options.Entry == "business" {
		if kind == "" {
			kind = cfg.Worker.Kind
		}
		settings, err := config.LoadBusiness(ctx, cfg.BusinessFile, cfg.Environment, kind)
		if err != nil {
			return err
		}
		if err = CheckBusinessWorkerConfig(settings, cfg.Environment, kind); err != nil {
			return err
		}
		if options.CheckConfig {
			return nil
		}
		return RunBusinessWorker(ctx, settings, cfg.Environment, kind, options.Check, options.Recover, log)
	}
	// 旧入口没有诊断参数，不允许内部调用者借用该组合跳过装配。
	if options.Check || options.CheckConfig {
		return command.ErrInvalid
	}
	switch options.Entry {
	case "card":
		if kind != "card" {
			return command.ErrInvalid
		}
	case "content":
		if kind != "content" && kind != "whispers" {
			return command.ErrInvalid
		}
		if kind == "whispers" {
			if cfg.WhispersFile == "" {
				return command.ErrInvalid
			}
			return RunWhisperContent(ctx, cfg.WhispersFile, cfg.Environment, log)
		}
	case "privacy":
		if cfg.WhispersFile != "" {
			return RunWhisperPrivacy(ctx, cfg.WhispersFile, cfg.Environment, kind, options.Recover, log)
		}
	default:
		return command.ErrInvalid
	}
	development, err := LoadDevelopment(cfg.DevAppFile)
	if err != nil {
		return err
	}
	switch options.Entry {
	case "card":
		return RunCardWorker(ctx, cfg.Environment, development, log)
	case "content":
		return RunContentWorker(
			ctx,
			cfg.Environment,
			development,
			ContentWorkerConfig{Analyzer: cfg.Worker.ContentAnalyzer},
			log,
		)
	default:
		return RunPrivacyWorker(ctx, cfg.Environment, development, kind, log)
	}
}
