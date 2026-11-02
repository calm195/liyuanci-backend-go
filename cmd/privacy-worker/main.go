// 隐私入口按显式配置选择正式依赖装配或隔离开发模式；恢复清理先于读取和外发。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/whisperapp"
)

func main() {
	path := flag.String("config", "", "私有应用YAML配置")
	recoverOnly := flag.Bool("recover", false, "显式恢复清理，完成前不开放业务")
	kind := flag.String("kind", "account", "执行工作职责")
	flag.Parse()
	app, loadErr := config.Load(*path)
	if loadErr != nil {
		os.Exit(1)
	}
	logger, e := platform.NewLogger("info")
	if e != nil {
		os.Exit(1)
	}
	start := time.Now()
	if path := app.WhispersFile; path != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		e = whisperapp.RunPrivacy(ctx, path, app.Environment, *kind, *recoverOnly, logger)
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
	cfg, e := devapp.Load(app.DevAppFile)
	if e == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		e = devapp.RunPrivacyWorker(ctx, app.Environment, cfg, *kind, logger)
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
