// business-worker 运行完整业务的正式工作进程，不装配开发供应商或探针。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/whisperapp"
	"go.uber.org/zap"
)

func main() {
	path := flag.String("config", os.Getenv("APP_BUSINESSFILE"), "私有业务JSON配置")
	env := flag.String("environment", os.Getenv("APP_ENVIRONMENT"), "显式production或integration环境")
	kind := flag.String("kind", "", "card/content/whispers/account/evidence")
	offline := flag.Bool("check-config", false, "只验证配置，不连接数据库或供应商")
	check := flag.Bool("check", false, "验证依赖，不消费任务")
	recoverOnly := flag.Bool("recover", false, "只恢复账号或证据删除，完成前不得开放读取和外发")
	flag.Parse()
	log, e := platform.NewLogger("info")
	if e != nil {
		os.Exit(1)
	}
	start := time.Now()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	c, e := config.LoadBusiness(ctx, *path, *env, *kind)
	if e == nil {
		e = whisperapp.CheckBusinessWorkerConfig(c, *env, *kind)
	}
	if e == nil && !*offline {
		e = whisperapp.RunBusinessWorker(ctx, c, *env, *kind, *check, *recoverOnly, log)
	}
	stop()
	result := "success"
	if e != nil {
		result = "failure"
	}
	log.Info("业务工作入口结束", zap.String("kind", *kind), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	_ = log.Sync()
	if e != nil {
		os.Exit(1)
	}
}
