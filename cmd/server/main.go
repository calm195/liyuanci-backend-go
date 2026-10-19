package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "configs/config.yaml", "YAML configuration path; empty for environment only")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	log, err := platform.NewLogger(cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("log level: %w", err)
	}
	// Sync 在资源清理后执行，确保最后的生命周期日志也被刷新。
	// 无缓冲的标准输出可能不支持 fsync，此时没有待刷新的缓冲日志。
	defer func() { _ = log.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	resources, err := platform.Open(startup, cfg)
	cancel()
	if err != nil {
		// 客户端错误可能包含凭据，因此这里只记录操作结果。
		log.Error("resource initialization failed")
		return errors.New("resource initialization failed")
	}
	log.Info("resource initialization completed",
		zap.Bool("database_enabled", resources.DB != nil),
		zap.Bool("redis_enabled", resources.Redis != nil),
		zap.Bool("rocketmq_enabled", resources.Producer != nil),
	)
	defer func() {
		if err := resources.Close(); err != nil {
			log.Error("resource cleanup failed")
			return
		}
		log.Info("resource cleanup completed")
	}()
	server := &http.Server{
		Addr: cfg.Server.Address,
		// T004/REQ-018、020：基础依赖可连接不等于认证/密钥已就绪；T008前不向流量入口报业务ready。
		Handler: httpapi.New(log, func(ctx context.Context) error {
			if err := resources.Ready(ctx); err != nil {
				return err
			}
			return errors.New("authentication and key readiness not implemented (T008)")
		}),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       60 * time.Second,
	}
	// 先绑定监听地址再记录启动成功，避免地址冲突时误报就绪。
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Error("server startup failed", zap.Error(err))
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Info("server startup completed", zap.String("address", listener.Addr().String()))
	// T004/REQ-020：存活心跳独立于业务ready，停止函数等待日志协程退出。
	stopHeartbeat := platform.StartHeartbeat(ctx, log, "api")
	defer stopHeartbeat()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server serving failed", zap.Error(err))
			return err
		}
		return nil
	case <-ctx.Done():
	}
	// 先等待 HTTP 请求结束，再由延迟清理逻辑关闭共享客户端。
	log.Info("server shutdown started")
	shutdown, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		log.Error("server shutdown failed", zap.Error(err))
		return err
	}
	log.Info("server shutdown completed")
	return nil
}
