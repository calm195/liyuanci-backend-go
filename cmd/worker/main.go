// T004/REQ-020：独立worker目前仅允许隔离探针，不宣称T007领域可靠消费完成。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "configs/integration.yaml", "隔离环境配置路径")
	flag.Parse()
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	log, err := platform.NewLogger(c.Log.Level)
	if err != nil {
		return errors.New("logger configuration failed")
	}
	defer func() { _ = log.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	r, err := platform.Open(startup, c)
	cancel()
	if err != nil {
		log.Error("worker resources failed", zap.String("task_id", "T004"))
		return errors.New("worker resources failed")
	}
	defer func() {
		if err := r.Close(); err != nil {
			log.Error("worker resources cleanup failed")
		} else {
			log.Info("worker resources cleanup completed")
		}
	}()
	client, err := worker.Start(c, r, log)
	if err != nil {
		log.Error("consumer startup failed")
		return err
	}
	// 停止消费并等待回调结束后才能关闭数据库等共享依赖。
	defer func() {
		if err := client.Shutdown(); err != nil {
			log.Error("consumer shutdown failed")
		} else {
			log.Info("consumer shutdown completed")
		}
	}()
	// T004/REQ-020：消费者真实启动后才输出存活心跳，不代表T007业务消费验收。
	stopHeartbeat := platform.StartHeartbeat(ctx, log, "worker")
	defer stopHeartbeat()
	<-ctx.Done()
	return nil
}
