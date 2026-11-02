// 开发业务初始化只能由操作者显式执行，不作为服务启动副作用。
package main

import (
	"context"
	"flag"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
)

func main() {
	path := flag.String("config", "", "显式开发应用YAML")
	flag.Parse()
	app, err := config.Load(*path)
	if err != nil {
		os.Exit(1)
	}
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := devapp.Load(app.DevAppFile)
	if err == nil && (app.Environment == "integration" || app.Environment == "development") && app.Bootstrap.Login != "" {
		err = devapp.Initialize(ctx, cfg, logger, app.Bootstrap.Login)
	} else {
		logger.Error("缺少显式开发初始化配置")
		os.Exit(1)
	}
	if err != nil {
		logger.Error("开发业务初始化失败")
		os.Exit(1)
	}
}
