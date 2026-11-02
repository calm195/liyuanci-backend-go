// business-worker 运行完整业务的正式工作进程，不装配开发供应商或探针。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/whisperapp"
)

func main() {
	path := flag.String("config", "", "公共配置旁的私有环境YAML")
	offline := flag.Bool("check-config", false, "只验证配置，不连接数据库或供应商")
	check := flag.Bool("check", false, "验证依赖，不消费任务")
	recoverOnly := flag.Bool("recover", false, "只恢复账号或证据删除，完成前不得开放读取和外发")
	kind := flag.String("kind", "", "执行职责：card/content/whispers/account/evidence")
	flag.Parse()
	log, e := platform.NewLogger("info")
	if e != nil {
		os.Exit(1)
	}
	start := time.Now()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	app, e := config.Load(*path)
	if *kind != "" {
		app.Worker.Kind = *kind
	}
	var c config.Business
	if e == nil {
		c, e = config.LoadBusiness(ctx, app.BusinessFile, app.Environment, app.Worker.Kind)
	}
	if e == nil {
		e = whisperapp.CheckBusinessWorkerConfig(c, app.Environment, app.Worker.Kind)
	}
	if e == nil && !*offline {
		e = whisperapp.RunBusinessWorker(ctx, c, app.Environment, app.Worker.Kind, *check, *recoverOnly, log)
	}
	stop()
	result := "success"
	if e != nil {
		result = "failure"
	}
	log.Info("业务工作入口结束", zap.String("kind", app.Worker.Kind), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	_ = log.Sync()
	if e != nil {
		os.Exit(1)
	}
}
