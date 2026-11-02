// business-worker 保留原参数与信号边界，运行资源交由app管理。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/calm195/liyuanci-backend-go/internal/app"
)

func main() {
	if run() != nil {
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "", "公共配置旁的私有环境YAML")
	offline := flag.Bool("check-config", false, "只验证配置，不连接数据库或供应商")
	check := flag.Bool("check", false, "验证依赖，不消费任务")
	recoverOnly := flag.Bool("recover", false, "只恢复账号或证据删除，完成前不得开放读取和外发")
	kind := flag.String("kind", "", "执行职责：card/content/whispers/account/evidence")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.RunWorker(ctx, app.WorkerOptions{ConfigFile: *path, Entry: "business", Kind: *kind, Check: *check, CheckConfig: *offline, Recover: *recoverOnly})
}
