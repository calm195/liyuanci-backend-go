package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// ServerOptions 由命令行入口传入，只描述宿主行为；配置仍只从显式YAML读取。
type ServerOptions struct {
	ConfigFile         string
	Check, CheckConfig bool
}

// RunServer 装配并运行HTTP宿主；离线检查不打开任何外部依赖。
// ctx由入口的信号控制，取消后先等待请求结束，再逆序释放业务池、供应商和共享客户端。
// 本函数不迁移数据库、不初始化账号，也不替换历史密钥或删除意图。
func RunServer(ctx context.Context, options ServerOptions) error {
	if options.Check && options.CheckConfig {
		return errors.New("检查模式不能同时指定")
	}
	cfg, err := config.Load(options.ConfigFile)
	if err != nil {
		return err
	}
	if (options.Check || options.CheckConfig) && cfg.DevAppFile == "" && cfg.WhispersFile == "" && cfg.BusinessFile == "" {
		return errors.New("依赖诊断必须显式提供业务配置")
	}
	log, err := platform.NewLogger(cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("日志级别配置失败：%w", err)
	}
	// Sync 在资源清理后执行，确保最后的生命周期日志也被刷新。
	// 无缓冲的标准输出可能不支持 fsync，此时没有待刷新的缓冲日志。
	defer func() { _ = log.Sync() }()
	// 先完成各运行模式的配置投影与校验，离线诊断到此阶段不访问外部依赖。
	var businessSettings *config.Business
	if cfg.BusinessFile != "" {
		settings, e := config.LoadBusiness(ctx, cfg.BusinessFile, cfg.Environment, "api")
		if e != nil {
			return errors.New("完整业务配置无效或必需凭据缺失")
		}
		businessSettings = &settings
		cfg.Database.DSN = settings.Runtime.DatabaseDSN
	}
	var whisperSettings *WhisperSettings
	if cfg.WhispersFile != "" {
		settings, e := config.LoadWhispers(ctx, cfg.WhispersFile, cfg.Environment)
		if e != nil {
			return errors.New("树洞运行配置无效或启用被禁止")
		}
		whisperSettings = &settings
		if cfg.DevAppFile == "" {
			cfg.Database.DSN = settings.DatabaseDSN
		}
	}
	var development *DevelopmentSettings
	if cfg.DevAppFile != "" {
		settings, e := LoadDevelopment(cfg.DevAppFile)
		if e != nil {
			log.Error("开发业务配置读取失败")
			return errors.New("开发业务配置读取失败")
		}
		development = &settings
		// 开发业务进程的共享健康连接也使用业务运行身份，不能保留探针管理员连接。
		cfg.Database.DSN = settings.DatabaseDSN
	}
	if options.CheckConfig {
		log.Info("业务配置诊断完成", zap.String("result", "success"))
		return nil
	}
	// 从这里开始获取运行资源；后注册的业务依赖先关闭，共享资源最后释放。
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
	)
	defer func() {
		if err := resources.Close(); err != nil {
			log.Error("资源清理失败")
			return
		}
		log.Info("资源清理完成")
	}()
	var whisperAPI *WhisperAPI
	var businessAPI *BusinessAPI
	router := httpapi.New(log, func(ctx context.Context) error {
		if whisperAPI != nil {
			return whisperAPI.Ready(ctx)
		}
		if err := resources.Ready(ctx); err != nil {
			return err
		}
		return errors.New("学生认证与密钥就绪检查尚未实现")
	})
	if businessSettings != nil {
		initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		app, e := OpenBusiness(initCtx, *businessSettings, cfg.Environment, resources.Redis, log)
		cancel()
		if e != nil {
			return errors.New("完整业务依赖装配失败")
		}
		businessAPI = app
		defer func() { _ = app.Close() }()
		router = app.Router
	}
	if cfg.DevAppFile != "" {
		initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		businessRouter, closeBusiness, e := OpenDevelopment(initCtx, cfg.Environment, *development, resources.Redis, log, func(checkCtx context.Context) error {
			// 本地业务允许数据库轮询，就绪检查只覆盖实际装配依赖。
			// 开发宿主仍检查业务池，树洞存在时继续检查其真实装配依赖。
			if development.LocalTest {
				if resources.Redis == nil {
					return errors.New("本地共享限流不可用")
				}
				if e := resources.Redis.Ping(checkCtx).Err(); e != nil {
					return e
				}
			} else if e := resources.Ready(checkCtx); e != nil {
				return e
			}
			if whisperAPI != nil {
				return whisperAPI.Ready(checkCtx)
			}
			return nil
		})
		cancel()
		if e != nil {
			log.Error("开发业务装配失败，请显式初始化数据库和凭据")
			return errors.New("开发业务装配失败")
		}
		defer func() { _ = closeBusiness() }()
		router = businessRouter
	}
	if whisperSettings != nil {
		initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		app, e := OpenWhispers(initCtx, *whisperSettings, resources.Redis, log)
		cancel()
		if e != nil {
			return errors.New("树洞依赖装配失败")
		}
		defer func() { _ = app.Close() }()
		whisperAPI = app
		if e = app.Register(router); e != nil {
			return errors.New("树洞路由装配失败")
		}
	}
	// 依赖诊断复用真实装配，但不绑定监听端口，也不进入服务循环。
	if options.Check {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if development != nil {
			if err := CheckCardWorker(checkCtx, *development, log); err != nil {
				log.Error("开发工作进程诊断失败")
				return errors.New("开发工作进程诊断失败")
			}
		}
		if whisperAPI != nil && whisperAPI.Ready(checkCtx) != nil {
			return errors.New("树洞依赖诊断失败")
		}
		if businessAPI != nil && businessAPI.Ready(checkCtx) != nil {
			return errors.New("完整业务依赖诊断失败")
		}
		log.Info("业务依赖诊断完成", zap.String("result", "success"))
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
	return serveHTTP(ctx, server, cfg.Server.ShutdownTimeout, log)
}

// serveHTTP 绑定成功后才报告启动，收到取消后先排空请求再允许宿主释放业务依赖。
func serveHTTP(ctx context.Context, server *http.Server, shutdownTimeout time.Duration, log *zap.Logger) error {
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
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		log.Error("服务关闭失败", zap.Error(err))
		return err
	}
	log.Info("服务关闭完成")
	return nil
}
