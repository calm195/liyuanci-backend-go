// init 显式初始化，不作为普通启动副作用。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/calm195/liyuanci-backend-go/internal/app"
)

func run() error {
	path := flag.String("config", "configs/development.yaml", "公共配置旁的私有环境YAML")
	check := flag.Bool("check-config", false, "仅离线校验配置，不连接或初始化")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("不接受位置参数，请使用 -h 查看用法")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.RunInitialization(ctx, *path, *check)
}

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "初始化未完成，请核对参数、配置和依赖；已有数据保留")
		os.Exit(1)
	}
}
