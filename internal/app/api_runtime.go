package app

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// apiRuntime 拥有本API的业务资源，借用宿主Redis；共享结构检查与恢复准入不隶属于某个API入口。
type apiRuntime struct {
	r        *runtimeResources
	client   redis.UniversalClient
	database *gorm.DB
}

// openAPIRuntime 在构造服务前检查结构、资料、两域恢复门禁及共享限流依赖。
func openAPIRuntime(ctx context.Context, cfg RuntimeSettings, client redis.UniversalClient, log *zap.Logger) (out *apiRuntime, err error) {
	if log == nil || client == nil {
		return nil, command.ErrInvalid
	}
	defer finish(log, "api_runtime_open", time.Now(), &err)
	r, e := openRuntimeResources(ctx, cfg, log)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			_ = r.close()
		}
	}()
	a := &apiRuntime{r: r, client: client}
	db, e := r.pool(ctx)
	if e != nil {
		return nil, e
	}
	a.database = db
	if err = r.check(ctx, db); err != nil {
		return nil, err
	}
	if cfg.Enabled {
		if err = r.rules(ctx, db); err != nil {
			return nil, err
		}
	}
	if err = r.recoveryGate(ctx, db, "account"); err != nil {
		return nil, err
	}
	if err = r.recoveryGate(ctx, db, "evidence"); err != nil {
		return nil, err
	}
	if err = client.Ping(ctx).Err(); err != nil {
		return nil, command.ErrUnavailable
	}
	return a, nil
}

// ready 复核共享数据库、Redis、结构、词库和本机身份摘要；模块另行复核自己的配置。
func (a *apiRuntime) ready(ctx context.Context) error {
	pool, e := a.database.DB()
	if e != nil || pool.PingContext(ctx) != nil {
		return command.ErrUnavailable
	}
	if a.client.Ping(ctx).Err() != nil {
		return command.ErrUnavailable
	}
	if e := a.r.check(ctx, a.database); e != nil {
		return e
	}
	if a.r.cfg.Enabled {
		if e := a.r.rules(ctx, a.database); e != nil {
			return e
		}
	}
	if e := a.r.gateway.Ready(ctx); e != nil {
		return e
	}
	return nil
}

// close 由拥有该运行资源的API宿主在请求排空后调用，不关闭借用的Redis。
func (a *apiRuntime) close() error { return a.r.close() }

// openAPIComponents 组合共享运行资源和树洞服务；失败仅释放本次创建的资源。
// 完整业务和独立树洞入口都使用这个构造边界，彼此不嵌入对方的API宿主。
func openAPIComponents(ctx context.Context, cfg RuntimeSettings, client redis.UniversalClient, log *zap.Logger) (runtime *apiRuntime, services *whisperServices, err error) {
	if log == nil {
		return nil, nil, command.ErrInvalid
	}
	defer finish(log, "api_open", time.Now(), &err)
	runtime, err = openAPIRuntime(ctx, cfg, client, log)
	if err != nil {
		return nil, nil, err
	}
	opened := runtime
	defer func() {
		if err != nil {
			_ = opened.close()
			runtime, services = nil, nil
		}
	}()
	services, err = buildWhisperServices(ctx, runtime)
	if err != nil {
		return runtime, nil, err
	}
	return runtime, services, nil
}
