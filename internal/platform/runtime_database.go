package platform

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// OpenRuntimeDatabase 打开应用共用连接池，拒绝超级用户、集群管理权限和只读副本。
// 成功后关闭责任转交宿主；本函数不迁移或读取业务内容。
func OpenRuntimeDatabase(ctx context.Context, dsn string, log *zap.Logger) (out *gorm.DB, closePool func() error, err error) {
	return OpenRuntimeDatabaseWithLimits(ctx, dsn, log, 0, 0)
}

// OpenRuntimeDatabaseWithLimits 在相同权限检查下收紧应用池的预算；零值兼容10/2，不能无限连接。
func OpenRuntimeDatabaseWithLimits(ctx context.Context, dsn string, log *zap.Logger, maxOpen, maxIdle int, lifetimes ...time.Duration) (out *gorm.DB, closePool func() error, err error) {
	if maxOpen == 0 && maxIdle == 0 {
		maxOpen, maxIdle = 10, 2
	}
	if maxOpen < 1 || maxOpen > 100 || maxIdle < 0 || maxIdle > maxOpen {
		return nil, nil, command.ErrInvalid
	}
	if log == nil {
		return nil, nil, command.ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		log.Info("运行数据库连接完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if dsn == "" {
		return nil, nil, command.ErrInvalid
	}
	db, e := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if e != nil {
		return nil, nil, command.ErrUnavailable
	}
	p, e := db.DB()
	if e != nil {
		return nil, nil, command.ErrUnavailable
	}
	p.SetMaxOpenConns(maxOpen)
	p.SetMaxIdleConns(maxIdle)
	lifetime := 30 * time.Minute
	if len(lifetimes) > 0 && lifetimes[0] != 0 {
		lifetime = lifetimes[0]
	}
	if lifetime <= 0 {
		_ = p.Close()
		return nil, nil, command.ErrInvalid
	}
	p.SetConnMaxLifetime(lifetime)
	fail := func() (*gorm.DB, func() error, error) {

		_ = p.Close()
		return nil, nil, command.ErrUnavailable
	}
	if p.PingContext(ctx) != nil {
		return fail()
	}
	var safe bool
	e = db.WithContext(ctx).Raw(`SELECT NOT pg_is_in_recovery() AND current_user=session_user AND NOT r.rolsuper AND NOT r.rolbypassrls AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication FROM pg_roles r WHERE r.rolname=current_user`).
		Scan(&safe).
		Error
	if e != nil || !safe {
		return fail()
	}
	return db, p.Close, nil
}
