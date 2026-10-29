package platform

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// OpenLocalDatabase 仅为integration本地联调打开共用身份池，业务授权仍由会话及领域服务执行。
// 不接受管理员、DDL或迁移权限，不修改数据库权限或创建资源。
func OpenLocalDatabase(ctx context.Context, environment, dsn string, log *zap.Logger) (out *gorm.DB, closePool func() error, err error) {
	if environment != "integration" || log == nil || dsn == "" {
		return nil, nil, command.ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		log.Info("本地共用数据库连接完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	db, e := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		return nil, nil, command.ErrUnavailable
	}
	p, e := db.DB()
	if e != nil {
		return nil, nil, command.ErrUnavailable
	}
	p.SetMaxOpenConns(10)
	p.SetMaxIdleConns(2)
	p.SetConnMaxLifetime(30 * time.Minute)
	var safe bool
	e = db.WithContext(ctx).Raw(`SELECT NOT pg_is_in_recovery() AND current_user=session_user AND NOT r.rolsuper AND NOT r.rolbypassrls AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication
 AND NOT has_schema_privilege(current_user,'public','CREATE') AND NOT has_database_privilege(current_user,current_database(),'CREATE')
 AND NOT pg_has_role(current_user,'liyuanci_migrator','member')
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','evidence') AND c.relowner=r.oid)
 FROM pg_roles r WHERE r.rolname=current_user`).Scan(&safe).Error
	if e != nil || !safe {
		_ = p.Close()
		return nil, nil, command.ErrUnavailable
	}
	return db, p.Close, nil
}
