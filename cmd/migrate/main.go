// migrate 保留显式DDL操作和信号边界，普通启动不调用迁移宿主。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/calm195/liyuanci-backend-go/internal/app"
)

func main() { os.Exit(run()) }

func run() int {
	path := flag.String("config", "configs/development.yaml", "与API相同的环境覆盖YAML")
	flag.Parse()
	if flag.NArg() != 1 {
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if app.RunMigration(ctx, *path, flag.Arg(0)) != nil {
		return 1
	}
	return 0
}
