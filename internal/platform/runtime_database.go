package platform

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// OpenRuntimeDatabase 打开独立运行身份池，拒绝管理身份、跨角色继承、只读副本和DDL权限。
// 成功后关闭责任转交宿主；本函数不迁移或读取业务内容。
func OpenRuntimeDatabase(ctx context.Context, dsn, role string, log *zap.Logger) (out *gorm.DB, closePool func() error, err error) {
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
	p.SetMaxOpenConns(10)
	p.SetMaxIdleConns(2)
	p.SetConnMaxLifetime(30 * time.Minute)
	fail := func() (*gorm.DB, func() error, error) { _ = p.Close(); return nil, nil, command.ErrUnavailable }
	if p.PingContext(ctx) != nil {
		return fail()
	}
	var safe bool
	e = db.WithContext(ctx).Raw(`SELECT NOT pg_is_in_recovery() AND current_user=session_user AND NOT r.rolsuper AND NOT r.rolbypassrls AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication
 AND pg_has_role(current_user,?,'member') AND NOT has_schema_privilege(current_user,'public','CREATE')
 AND NOT has_database_privilege(current_user,current_database(),'CREATE')
 AND NOT EXISTS(SELECT 1 FROM pg_roles g WHERE g.rolname<>? AND g.rolname<>current_user AND pg_has_role(current_user,g.oid,'member'))
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','evidence') AND c.relowner=r.oid)
 FROM pg_roles r WHERE r.rolname=current_user`, role, role).Scan(&safe).Error
	if e != nil || !safe {
		return fail()
	}
	return db, p.Close, nil
}
