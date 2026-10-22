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

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
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
	path := flag.String("config", "", "YAML配置路径；留空时仅使用环境变量")
	check := flag.Bool("check", false, "仅检查开发业务依赖和装配，不监听HTTP")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *check && cfg.DevAppFile == "" {
		return errors.New("开发诊断必须显式提供业务配置")
	}
	log, err := platform.NewLogger(cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("日志级别配置失败：%w", err)
	}
	// Sync 在资源清理后执行，确保最后的生命周期日志也被刷新。
	// 无缓冲的标准输出可能不支持 fsync，此时没有待刷新的缓冲日志。
	defer func() { _ = log.Sync() }()
	var development *devapp.Settings
	if cfg.DevAppFile != "" {
		settings, e := devapp.Load(cfg.DevAppFile)
		if e != nil {
			log.Error("开发业务配置读取失败")
			return errors.New("开发业务配置读取失败")
		}
		development = &settings
		// 开发业务进程的共享健康连接也使用业务运行身份，不能保留探针管理员连接。
		cfg.Database.DSN = settings.WishDSN
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	resources, err := platform.Open(startup, cfg)
	cancel()
	if err != nil {
		// 客户端错误可能包含凭据，因此这里只记录操作结果。
		log.Error("资源初始化失败")
		return errors.New("资源初始化失败")
	}
	log.Info("资源初始化完成",
		zap.Bool("database_enabled", resources.DB != nil),
		zap.Bool("redis_enabled", resources.Redis != nil),
		zap.Bool("rocketmq_enabled", resources.Producer != nil),
	)
	defer func() {
		if err := resources.Close(); err != nil {
			log.Error("资源清理失败")
			return
		}
		log.Info("资源清理完成")
	}()
	router := httpapi.New(log, func(ctx context.Context) error {
		if err := resources.Ready(ctx); err != nil {
			return err
		}
		return errors.New("学生认证与密钥就绪检查尚未实现")
	})
	if cfg.DevAppFile != "" {
		initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		businessRouter, closeBusiness, e := devapp.Open(initCtx, cfg.Environment, *development, resources.Redis, log, resources.Ready)
		cancel()
		if e != nil {
			log.Error("开发业务装配失败，请显式初始化数据库和凭据")
			return errors.New("开发业务装配失败")
		}
		defer func() { _ = closeBusiness() }()
		router = businessRouter
	}
	if cfg.AdminAuth.Enabled {
		initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		service, closePool, e := adminauth.Open(initCtx, cfg, resources.Redis, log)
		cancel()
		if e != nil {
			return errors.New("后台身份资源初始化失败")
		}
		defer func() { _ = closePool() }()
		if e = httpapi.RegisterAdmin(router, service); e != nil {
			return errors.New("后台身份路由装配失败")
		}
	}
	if *check {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := devapp.CheckCardWorker(checkCtx, *development, log); err != nil {
			log.Error("开发工作进程诊断失败")
			return errors.New("开发工作进程诊断失败")
		}
		log.Info("开发业务诊断完成", zap.String("result", "success"))
		return nil
	}
	server := &http.Server{
		Addr: cfg.Server.Address,
		// 后台独立入口可用不等于学生认证/密钥已就绪，readyz仍按整体业务依赖失败关闭。
		Handler:           router,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       60 * time.Second,
	}
	// 先绑定监听地址再记录启动成功，避免地址冲突时误报就绪。
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Error("服务启动失败", zap.Error(err))
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Info("服务启动完成", zap.String("address", listener.Addr().String()))
	// 存活心跳独立于业务ready，停止函数等待日志协程退出。
	stopHeartbeat := platform.StartHeartbeat(ctx, log, "api")
	defer stopHeartbeat()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("服务运行失败", zap.Error(err))
			return err
		}
		return nil
	case <-ctx.Done():
	}
	// 先等待 HTTP 请求结束，再由延迟清理逻辑关闭共享客户端。
	log.Info("开始关闭服务")
	shutdown, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		log.Error("服务关闭失败", zap.Error(err))
		return err
	}
	log.Info("服务关闭完成")
	return nil
}
