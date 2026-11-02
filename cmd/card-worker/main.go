// 开发学生卡工作入口仅允许隔离环境，扫描结论为模拟通过。
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
)

func main() {
	path := flag.String("config", "", "私有应用YAML配置")
	kind := flag.String("kind", "card", "执行工作职责")
	flag.Parse()
	app, loadErr := config.Load(*path)
	if loadErr != nil || *kind != "card" {
		os.Exit(1)
	}
	logger, err := platform.NewLogger("info")
	if err != nil {
		os.Exit(1)
	}
	start := time.Now()
	cfg, err := devapp.Load(app.DevAppFile)
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = devapp.RunCardWorker(ctx, app.Environment, cfg, logger)
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
