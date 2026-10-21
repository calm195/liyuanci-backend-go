package adminauth

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
)

// Open 创建独立后台连接池，读取仓库外凭据；返回的关闭函数只在调用方等待所有请求退出后执行。
// 不执行迁移、权限配置或创建账号，生产参数缺失时由配置校验拒绝启用。
func Open(ctx context.Context, c config.Config, client redis.UniversalClient, logger *zap.Logger) (service *Service, closePool func() error, err error) {
	if logger == nil {
		return nil, nil, ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("后台身份资源开启完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if !c.AdminAuth.Enabled {
		return nil, nil, ErrInvalid
	}
	password, e := os.ReadFile(c.AdminAuth.InitialPasswordFile)
	if e != nil {
		return nil, nil, ErrUnavailable
	}
	defer clear(password)
	key, e := os.ReadFile(c.AdminAuth.ReceiptKeyFile)
	if e != nil {
		return nil, nil, ErrUnavailable
	}
	defer clear(key)
	limiter, e := NewRedisLimiter(client, c.AdminAuth.LoginWindow, c.AdminAuth.LoginLimit)
	if e != nil {
		return nil, nil, e
	}
	db, e := gorm.Open(postgres.Open(c.AdminAuth.DSN), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent), DisableAutomaticPing: true})
	if e != nil {
		return nil, nil, ErrUnavailable
	}
	pool, e := db.DB()
	if e != nil {
		return nil, nil, ErrUnavailable
	}
	pool.SetMaxOpenConns(c.Database.MaxOpen)
	pool.SetMaxIdleConns(c.Database.MaxIdle)
	pool.SetConnMaxLifetime(c.Database.MaxLifetime)
	if e = pool.PingContext(ctx); e != nil {
		_ = pool.Close()
		return nil, nil, ErrUnavailable
	}
	// 普通服务只接受专用维护身份；拒绝超级用户、表属主及绕过RLS的连接冒充隔离。
	var safe bool
	e = db.WithContext(ctx).Raw(`SELECT NOT r.rolsuper AND NOT r.rolbypassrls AND NOT r.rolcreatedb AND NOT r.rolcreaterole
AND NOT EXISTS (SELECT 1 FROM pg_roles owner_role WHERE owner_role.rolname=session_user AND (owner_role.rolsuper OR owner_role.rolbypassrls OR owner_role.rolcreatedb OR owner_role.rolcreaterole))
AND NOT EXISTS (SELECT 1 FROM pg_roles other_role WHERE other_role.rolname IN ('liyuanci_migrator','liyuanci_api','liyuanci_worker','liyuanci_identity_maintenance','liyuanci_admin_api','liyuanci_domain_worker','liyuanci_privacy_maintenance','liyuanci_evidence_reader','liyuanci_evidence_writer','liyuanci_evidence_purger') AND pg_has_role(session_user,other_role.oid,'member'))
AND pg_has_role(current_user,'liyuanci_admin_maintenance','member') AND current_user<>pg_get_userbyid(c.relowner)
FROM pg_roles r JOIN pg_class c ON c.oid='public.admin_accounts'::regclass WHERE r.rolname=current_user`).Scan(&safe).Error
	if e != nil || !safe {
		_ = pool.Close()
		return nil, nil, ErrUnavailable
	}
	if e = checkSchema(ctx, db); e != nil {
		_ = pool.Close()
		return nil, nil, ErrUnavailable
	}
	service, e = New(db, limiter, logger, Config{
		SessionTTL:      c.AdminAuth.SessionTTL,
		InitialPassword: strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"),
		ReceiptKey:      key,
		Command: command.Config{
			Timeout:     c.AdminAuth.Timeout,
			LockTimeout: c.AdminAuth.LockTimeout,
			ReceiptTTL:  c.AdminAuth.ReceiptTTL,
			MaxAttempts: c.AdminAuth.MaxAttempts,
		},
	})
	if e != nil {
		_ = pool.Close()
		return nil, nil, e
	}
	closePool = func() (err error) {
		start := time.Now()
		defer func() {
			result := "success"
			if err != nil {
				result = "failure"
			}
			logger.Info("后台身份资源关闭完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
		}()
		if pool.Close() != nil {
			return ErrUnavailable
		}
		return nil
	}
	return service, closePool, nil
}
