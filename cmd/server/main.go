// server 保留命令行和信号边界，应用资源由app统一装配与关闭。
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "configs/development.yaml", "应用YAML配置路径")
	check := flag.Bool("check", false, "仅检查显式业务依赖和装配，不监听HTTP")
	checkConfig := flag.Bool("check-config", false, "只检查所选业务YAML，不连接外部依赖")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.RunServer(ctx, app.ServerOptions{ConfigFile: *path, Check: *check, CheckConfig: *checkConfig})
}
