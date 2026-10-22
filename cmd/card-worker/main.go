// 开发学生卡工作入口仅允许隔离环境，扫描结论为模拟通过。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/cardworker"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

func main() {
	logger, err := platform.NewLogger("info")
	if err != nil {
		os.Exit(1)
	}
	start := time.Now()
	cfg, err := devapp.Load(os.Getenv("APP_DEVAPPFILE"))
	if path := os.Getenv("APP_CARD_MQFILE"); err == nil && path != "" {
		cfg.CardMQ = &cardworker.Config{}
		err = config.LoadCardWorkerMQ(path, cfg.CardMQ)
		if err == nil {
			err = cardworker.Validate(*cfg.CardMQ)
		}
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = devapp.RunCardWorker(ctx, os.Getenv("APP_ENVIRONMENT"), cfg, logger)
		stop()
	}
	result := "success"
	if err != nil {
		result = "failure"
	}
	logger.Info("开发学生卡工作入口结束", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	_ = logger.Sync()
	if err != nil {
		os.Exit(1)
	}
}
