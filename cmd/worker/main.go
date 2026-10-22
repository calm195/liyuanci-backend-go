// 独立worker目前仅允许隔离探针，领域可靠消费尚未实现。
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
	path := flag.String("config", "configs/app/integration.yaml", "隔离环境配置路径")
	flag.Parse()
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	log, err := platform.NewLogger(c.Log.Level)
	if err != nil {
		return errors.New("日志配置失败")
	}
	defer func() { _ = log.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	r, err := platform.Open(startup, c)
	cancel()
	if err != nil {
		log.Error("工作进程资源初始化失败")
		return errors.New("工作进程资源初始化失败")
	}
	defer func() {
		if err := r.Close(); err != nil {
			log.Error("工作进程资源清理失败")
		} else {
			log.Info("工作进程资源清理完成")
		}
	}()
	client, err := worker.Start(c, r, log)
	if err != nil {
		log.Error("消费者启动失败")
		return err
	}
	// 停止消费并等待回调结束后才能关闭数据库等共享依赖。
	defer func() {
		if err := client.Shutdown(); err != nil {
			log.Error("消费者关闭失败")
		} else {
			log.Info("消费者关闭完成")
		}
	}()
	// 消费者真实启动后才输出存活心跳，不代表业务消费验收完成。
	stopHeartbeat := platform.StartHeartbeat(ctx, log, "worker")
	defer stopHeartbeat()
	<-ctx.Done()
	return nil
}
