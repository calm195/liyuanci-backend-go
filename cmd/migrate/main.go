// migrate 保留显式DDL操作和信号边界，普通启动不调用迁移宿主。
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

func main() { os.Exit(run()) }

func run() int {
	path := flag.String("config", "configs/development.yaml", "与API相同的环境覆盖YAML")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "迁移未完成，请指定有效操作并核对配置和依赖")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if app.RunMigration(ctx, *path, flag.Arg(0)) != nil {
		fmt.Fprintln(os.Stderr, "迁移未完成，请指定有效操作并核对配置和依赖")
		return 1
	}
	return 0
}
