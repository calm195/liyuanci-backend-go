package whisperapp

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

// applicationDatabase 用隔离测试管理员创建普通应用账号；实际迁移、初始化和运行共用其单DSN。
func applicationDatabase(t *testing.T, adminDSN string) (*gorm.DB, string, string) {
	t.Helper()
	open := func(dsn string) *gorm.DB {
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if e != nil {
			t.Fatal("测试数据库连接失败")
		}
		return db
	}
	parent := open(adminDSN)
	var parentName string
	if parent.Raw("SELECT current_database()").Scan(&parentName).Error != nil || (parentName != "liyuanci" && parentName != "whisper_test") {
		t.Fatal("仅允许显式隔离父库")
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	name, user, password := "app_"+suffix, "login_"+suffix, uuid.NewString()
	if parent.Exec("CREATE ROLE "+user+" LOGIN PASSWORD '"+password+"'").Error != nil || parent.Exec("CREATE DATABASE "+name+" OWNER "+user).Error != nil {
		t.Fatal("测试应用账号创建失败")
	}
	databaseAdmin := strings.Replace(adminDSN, "dbname="+parentName+" ", "dbname="+name+" ", 1)
	parts := strings.Fields(databaseAdmin)
	for i, p := range parts {
		if strings.HasPrefix(p, "user=") {
			parts[i] = "user=" + user
		}
		if strings.HasPrefix(p, "password=") {
			parts[i] = "password=" + password
		}
	}
	shared := strings.Join(parts, " ")
	db := open(shared)
	t.Cleanup(func() {
		p, _ := db.DB()
		_ = p.Close()
		_ = parent.Exec("DROP DATABASE " + name + " WITH (FORCE)").Error
		_ = parent.Exec("DROP ROLE " + user).Error
		p, _ = parent.DB()
		_ = p.Close()
	})
	ctx, log := context.Background(), zap.NewNop()
	if migration.Run(ctx, db, log, "up") != nil || migration.ApplyApplicationPermissions(ctx, db, log) != nil {
		t.Fatal("单应用账号迁移/权限配置失败")
	}
	return db, shared, databaseAdmin
}
