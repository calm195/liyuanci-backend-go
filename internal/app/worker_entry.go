package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// WorkerOptions 描述任务职责与互斥运行模式；适配器由已校验的YAML选择。
type WorkerOptions struct {
	ConfigFile, Kind            string
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
			zap.String("entry", "worker"),
			zap.String("kind", kind),
			zap.String("result", result),
			zap.Duration("duration", time.Since(start)),
		)
	}()
	cfg, err := config.Load(options.ConfigFile)
	if err != nil {
		return err
	}
	if kind == "" {
		kind = cfg.Worker.Kind
	}
	if err = validateWorkerOptions(options, kind); err != nil {
		return err
	}
	if cfg.Mode == "business" {
		settings := cfg.Business
		err := config.ValidateBusiness(settings, cfg.Environment, kind)
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
	// 树洞与删除职责复用相同装配，诊断必须在消费或恢复之前返回。
	if (cfg.Mode == "whispers" || cfg.Mode == "development" && cfg.Whispers.Enabled) && (kind == "whispers" || kind == "account" || kind == "evidence") {
		settings, err := config.ValidateWhispers(cfg.Whispers, cfg.Environment)
		if err != nil {
			return err
		}
		if options.CheckConfig {
			return nil
		}
		if kind == "whispers" {
			return runContentSettings(ctx, settings, cfg.Environment, log, options.Check)
		}
		return runPrivacySettings(ctx, settings, kind, options.Recover, log, options.Check)
	}
	if cfg.Mode != "development" || (cfg.Environment != "development" && cfg.Environment != "integration") || kind == "whispers" {
		return command.ErrInvalid
	}
	development, err := developmentSettings(cfg)
	if err != nil {
		return err
	}
	content := ContentWorkerConfig{Check: options.Check}
	if kind == "content" {
		if err := ValidateContentWorker(cfg.Environment, development); err != nil {
			return err
		}
	}
	if options.CheckConfig {
		return nil
	}
	switch kind {
	case "card":
		return runCardWorker(ctx, cfg.Environment, development, log, options.Check)
	case "content":
		return RunContentWorker(ctx, cfg.Environment, development, content, log)
	default:
		return RunPrivacyWorker(ctx, cfg.Environment, development, kind, log, options.Check, options.Recover)
	}
}

// validateWorkerOptions 先拒绝歧义组合，防止检查请求意外触发恢复或消费。
func validateWorkerOptions(options WorkerOptions, kind string) error {
	switch kind {
	case "card", "content", "whispers", "account", "evidence":
	default:
		return command.ErrInvalid
	}
	modes := 0
	for _, enabled := range []bool{options.Check, options.CheckConfig, options.Recover} {
		if enabled {
			modes++
		}
	}
	if modes > 1 || (options.Recover && kind != "account" && kind != "evidence") {
		return command.ErrInvalid
	}
	return nil
}
