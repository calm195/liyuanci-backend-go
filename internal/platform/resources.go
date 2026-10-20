package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	rocketmq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/config"
)

// Resources 管理可选中间件客户端及其生命周期。
type Resources struct {
	DB          *gorm.DB
	Redis       *redis.Client
	Producer    rocketmq.Producer
	NameServers []string
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
			Addr:         c.Redis.Address,
			Password:     c.Redis.Password,
			DB:           c.Redis.DB,
			DialTimeout:  3 * time.Second,
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
		})
		if err = r.Redis.Ping(ctx).Err(); err != nil {
			return nil, fmt.Errorf("Redis连接检查失败：%w", err)
		}
	}
	if c.RocketMQ.Enabled {
		r.NameServers, err = resolveNameServers(ctx, c.RocketMQ.NameServers, net.DefaultResolver)
		if err != nil {
			return nil, err
		}
		// SDK构造失败可返回含nil指针的interface。
		// 仅在构造和Start都成功后接管Producer，避免失败清理再次panic并遮蔽原始错误。
		client, createErr := rocketmq.NewProducer(
			producer.WithNameServer(primitive.NamesrvAddr(r.NameServers)),
			producer.WithGroupName(c.RocketMQ.Group),
			producer.WithRetry(2),
		)
		if createErr != nil {
			return nil, fmt.Errorf("创建生产者失败：%w", createErr)
		}
		if err = client.Start(); err != nil {
			_ = client.Shutdown()
			return nil, fmt.Errorf("启动生产者失败：%w", err)
		}
		r.Producer = client
	}
	return r, nil
}

// Ready 检查已启用的数据库和 Redis 连接是否可用。
func (r *Resources) Ready(ctx context.Context) error {
	// 缺少真实依赖时仅存活，不报告具备业务处理条件。
	if r.DB == nil || r.Redis == nil || r.Producer == nil {
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
func (r *Resources) Close() error {
	var errs []error
	if r.Producer != nil {
		errs = append(errs, r.Producer.Shutdown())
	}
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
