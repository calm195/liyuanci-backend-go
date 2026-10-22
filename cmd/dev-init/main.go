// 开发业务初始化只能由操作者显式执行，不作为服务启动副作用。
package main

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/devapp"
)

func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := devapp.Load(os.Getenv("APP_DEVAPPFILE"))
	if err == nil && os.Getenv("APP_ENVIRONMENT") == "integration" && os.Getenv("APP_ADMIN_BOOTSTRAP_LOGIN") != "" {
		err = devapp.Initialize(ctx, cfg, logger, os.Getenv("APP_ADMIN_BOOTSTRAP_LOGIN"))
	} else {
		logger.Error("缺少显式开发初始化配置")
		os.Exit(1)
	}
	if err != nil {
		logger.Error("开发业务初始化失败")
		os.Exit(1)
	}
}
