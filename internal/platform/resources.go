package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/config"
)

// Resources 管理可选中间件客户端及其生命周期。
type Resources struct {
	DB    *gorm.DB
	Redis *redis.Client
}

// Open 初始化已启用的客户端，失败时释放已创建的资源。
func Open(ctx context.Context, c config.Config) (_ *Resources, err error) {
	r := &Resources{}
	defer func() {
		if err != nil {
			_ = r.Close()
		}
	}()
	if c.Database.Driver != "" {
		dialector := postgres.Open(c.Database.DSN)
		r.DB, err = gorm.Open(dialector, &gorm.Config{
			Logger:               logger.Default.LogMode(logger.Silent),
			DisableAutomaticPing: true,
		})
		if err != nil {
			return nil, fmt.Errorf("打开数据库失败：%w", err)
		}
		db, e := r.DB.DB()
		if e != nil {
			return nil, e
		}
		db.SetMaxOpenConns(c.Database.MaxOpen)
		db.SetMaxIdleConns(c.Database.MaxIdle)
		db.SetConnMaxLifetime(c.Database.MaxLifetime)
		if err = db.PingContext(ctx); err != nil {
			return nil, fmt.Errorf("数据库连接检查失败：%w", err)
		}
	}
	if c.Redis.Enabled {
		r.Redis = redis.NewClient(&redis.Options{
			Addr:                  c.Redis.Address,
			Username:              c.Redis.Username,
			Password:              c.Redis.Password,
			DB:                    c.Redis.DB,
			DialTimeout:           3 * time.Second,
			ReadTimeout:           3 * time.Second,
			WriteTimeout:          3 * time.Second,
			ContextTimeoutEnabled: true,
		})
		if err = r.Redis.Ping(ctx).Err(); err != nil {
			return nil, fmt.Errorf("Redis连接检查失败：%w", err)
		}
	}
	return r, nil
}

// Ready 要求数据库和Redis均已装配且连接可用；只有健康端点的进程不报告业务就绪。
func (r *Resources) Ready(ctx context.Context) error {
	// 缺少真实依赖时仅存活，不报告具备业务处理条件。
	if r.DB == nil || r.Redis == nil {
		return errors.New("必需的中间件不可用")
	}
	if r.DB != nil {
		db, err := r.DB.DB()
		if err != nil {
			return err
		}
		if err = db.PingContext(ctx); err != nil {
			return err
		}
	}
	if r.Redis != nil {
		return r.Redis.Ping(ctx).Err()
	}
	return nil
}

// Close 按初始化的逆序释放客户端。
// 宿主须先等待借用这些客户端的请求和任务退出，本方法不负责排空业务。
func (r *Resources) Close() error {
	var errs []error
	if r.Redis != nil {
		errs = append(errs, r.Redis.Close())
	}
	if r.DB != nil {
		db, err := r.DB.DB()
		if err != nil {
			errs = append(errs, err)
		} else {
			errs = append(errs, db.Close())
		}
	}
	return errors.Join(errs...)
}
