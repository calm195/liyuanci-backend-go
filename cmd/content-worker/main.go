// 内容入口按显式配置选择正式树洞装配或隔离开发模式；生产树洞启用保持禁止。
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
	kind := flag.String("kind", "content", "执行工作职责")
	flag.Parse()
	app, loadErr := config.Load(*path)
	if loadErr != nil || (*kind != "content" && *kind != "whispers") || (*kind == "whispers" && app.WhispersFile == "") {
		os.Exit(1)
	}
	logger, err := platform.NewLogger("info")
	if err != nil {
		os.Exit(1)
	}
	start := time.Now()
	if path := app.WhispersFile; path != "" && *kind == "whispers" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = whisperapp.RunContent(ctx, path, app.Environment, logger)
		stop()
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("树洞内容入口结束", zap.String("result", result), zap.Duration("duration", time.Since(start)))
		_ = logger.Sync()
		if err != nil {
			os.Exit(1)
		}
		return
	}
	cfg, err := devapp.Load(app.DevAppFile)
	options := devapp.ContentWorkerConfig{Analyzer: app.Worker.ContentAnalyzer}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = devapp.RunContentWorker(ctx, app.Environment, cfg, options, logger)
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
