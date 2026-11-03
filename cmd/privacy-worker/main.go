// privacy-worker 保留原参数与信号边界，运行资源交由app管理。
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
	path := flag.String("config", "", "私有应用YAML配置")
	recoverOnly := flag.Bool("recover", false, "显式恢复清理，完成前不开放业务")
	kind := flag.String("kind", "account", "执行工作职责")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.RunWorker(ctx, app.WorkerOptions{ConfigFile: *path, Entry: "privacy", Kind: *kind, Recover: *recoverOnly})
}
