// worker 统一任务参数与信号边界，运行资源交由app管理。
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
		fmt.Fprintln(os.Stderr, "工作进程未完成，请核对参数、配置和依赖")
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "configs/development.yaml", "公共配置旁的私有环境YAML")
	offline := flag.Bool("check-config", false, "只验证配置，不连接数据库或供应商")
	check := flag.Bool("check", false, "验证依赖，不消费任务")
	recoverOnly := flag.Bool("recover", false, "只恢复账号或证据删除，完成前不得开放读取和外发")
	kind := flag.String("kind", "", "执行职责：card/content/whispers/account/evidence")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("不接受位置参数，请使用 -h 查看用法")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.RunWorker(ctx, app.WorkerOptions{
		ConfigFile:  *path,
		Kind:        *kind,
		Check:       *check,
		CheckConfig: *offline,
		Recover:     *recoverOnly,
	})
}
