// 开发内容工作入口仅允许隔离环境；默认无供应商失败关闭，合成或原文人工复核模式必须显式启用。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

func main() {
	logger, err := platform.NewLogger("info")
	if err != nil {
		os.Exit(1)
	}
	start := time.Now()
	cfg, err := devapp.Load(os.Getenv("APP_DEVAPPFILE"))
	options := devapp.ContentWorkerConfig{Analyzer: os.Getenv("APP_CONTENT_ANALYZER")}
	if path := os.Getenv("APP_CONTENT_MQFILE"); err == nil && path != "" {
		options.MQ = &workrunner.Config{}
		err = config.LoadCardWorkerMQ(path, options.MQ)
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = devapp.RunContentWorker(ctx, os.Getenv("APP_ENVIRONMENT"), cfg, options, logger)
		stop()
	}
	result := "success"
	if err != nil {
		result = "failure"
	}
	logger.Info("开发内容工作入口结束", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	_ = logger.Sync()
	if err != nil {
		os.Exit(1)
	}
}
