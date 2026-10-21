package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// 显式离线入口不能由普通启动脚本调用；配置提供凭据文件，不接受命令行口令。
func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "后台初始化未完成")
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "configs/config.yaml", "配置路径")
	flag.Parse()
	c, err := config.Load(*path)
	if err != nil {
		return errors.New("配置无效")
	}
	logger, err := platform.NewLogger(c.Log.Level)
	if err != nil {
		return errors.New("日志配置无效")
	}
	defer func() { _ = logger.Sync() }()
	if !c.AdminAuth.Enabled || os.Getenv("APP_ADMIN_BOOTSTRAP_LOGIN") == "" {
		return errors.New("必须显式提供后台配置和初始化登录名")
	}
	// 缓存只用于服务构造，本入口不运行登录或尝试放宽共享限流。
	client := redis.NewClient(&redis.Options{Addr: c.Redis.Address, Password: c.Redis.Password, DB: c.Redis.DB})
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), c.AdminAuth.Timeout)
	defer cancel()
	service, closePool, err := adminauth.Open(ctx, c, client, logger)
	if err != nil {
		return err
	}
	defer func() { _ = closePool() }()
	_, err = service.Bootstrap(ctx, os.Getenv("APP_ADMIN_BOOTSTRAP_LOGIN"))
	return err
}
