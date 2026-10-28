// 隐私入口按显式配置选择正式依赖装配或隔离开发模式；恢复清理先于读取和外发。
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
	"github.com/calm195/liyuanci-backend-go/internal/whisperapp"
)

func main() {
	logger, e := platform.NewLogger("info")
	if e != nil {
		os.Exit(1)
	}
	start := time.Now()
	if path := os.Getenv("APP_WHISPERSFILE"); path != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		e = whisperapp.RunPrivacy(ctx, path, os.Getenv("APP_ENVIRONMENT"), os.Getenv("APP_PRIVACY_SCOPE"), os.Getenv("APP_PRIVACY_RECOVER") == "true", logger)
		stop()
		result := "success"
		if e != nil {
			result = "failure"
		}
		logger.Info("树洞隐私入口结束", zap.String("result", result), zap.Duration("duration", time.Since(start)))
		_ = logger.Sync()
		if e != nil {
			os.Exit(1)
		}
		return
	}
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
