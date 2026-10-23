// 隐私工作入口仅面向显式隔离开发资源，正式环境通过注入供应商装配领域调度。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/devapp"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

func main() {
	logger, e := platform.NewLogger("info")
	if e != nil {
		os.Exit(1)
	}
	start := time.Now()
	cfg, e := devapp.Load(os.Getenv("APP_DEVAPPFILE"))
	if e == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		e = devapp.RunPrivacyWorker(ctx, os.Getenv("APP_ENVIRONMENT"), cfg, os.Getenv("APP_PRIVACY_SCOPE"), logger)
		stop()
	}
	result := "success"
	if e != nil {
		result = "failure"
	}
	logger.Info("开发隐私工作入口结束", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	_ = logger.Sync()
	if e != nil {
		os.Exit(1)
	}
}
