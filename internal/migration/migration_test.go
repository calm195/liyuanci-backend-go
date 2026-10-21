package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestInvalidDependencies(t *testing.T) {
	// T005/REQ-020：缺少依赖不能进入迁移。
	if Run(context.Background(), nil, zap.NewNop(), "up") == nil {
		t.Fatal("缺少数据库应拒绝")
	}
}

func TestPostgresMigrations(t *testing.T) {
	// T005/REQ-020：只允许专用 Docker 测试库；普通单元检查不连接中间件。
	dsn := os.Getenv("APP_MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式专用 PostgreSQL；运行 python3 scripts/check-migrations.py")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("测试数据库连接失败")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	core, observed := observer.New(zap.InfoLevel)
	log := zap.New(core)
	ctx := context.Background()
	// T005/REQ-020：模拟已验收的旧版账本，只追加新版本，不改写旧文件及哈希。
	firstUp, err := files.ReadFile("sql/0001_accounts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	firstDown, err := files.ReadFile("sql/0001_accounts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	firstSum := sha256.Sum256(append(append([]byte(nil), firstUp...), firstDown...))
	if err = db.Exec(string(firstUp)).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("CREATE TABLE public.schema_migrations(version bigint PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP)").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT INTO public.schema_migrations(version,checksum) VALUES (1,?)", hex.EncodeToString(firstSum[:])).Error; err != nil {
		t.Fatal(err)
	}
	failures := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- Run(ctx, db, log, "up") }()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	// T005/REQ-020：账本版本不变时也执行实体同步，补齐缺失常规字段及数据库注释。
	if err = db.Exec("ALTER TABLE public.accounts DROP COLUMN hide_identity_tags").Error; err != nil {
		t.Fatal(err)
	}
	if err = Run(ctx, db, log, "up"); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn("public.accounts", "hide_identity_tags") {
		t.Fatal("自动迁移未补齐实体字段")
	}
	// T005/REQ-020：数据库对象注释是强制规范，探针与迁移账本同样纳入验证。
	probeSQL, err := os.ReadFile("../../migrations/integration/0001_probe.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(string(probeSQL)).Error; err != nil {
		t.Fatal(err)
	}
	var missingComments int64
	if err = db.Raw(`SELECT count(*) FROM (
 SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname IN ('public', 'evidence') AND c.relkind IN ('r', 'p') AND nullif(btrim(pg_catalog.obj_description(c.oid, 'pg_class')), '') IS NULL
 UNION ALL
 SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
 WHERE n.nspname IN ('public', 'evidence') AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped
 AND nullif(btrim(pg_catalog.col_description(c.oid, a.attnum)), '') IS NULL
) missing`).Scan(&missingComments).Error; err != nil || missingComments != 0 {
		t.Fatalf("表或字段缺少数据库注释：%d，错误：%v", missingComments, err)
	}
	var count int64
	if err = db.Table("schema_migrations").Count(&count).Error; err != nil || count != 10 {
		t.Fatalf("迁移账本版本数量不符：%v", err)
	}
	for _, statement := range []string{
		"INSERT INTO accounts(id,nickname,lifecycle) VALUES ('00000000-0000-0000-0000-000000000001','合成','bad')",
		"INSERT INTO accounts(id,nickname,auth_epoch) VALUES ('00000000-0000-0000-0000-000000000001','合成',0)",
		"INSERT INTO accounts(id,nickname) VALUES ('00000000-0000-0000-0000-000000000001','   ')",
	} {
		if db.Exec(statement).Error == nil {
			t.Fatal("非法账号应被约束拒绝")
		}
	}
	if err = db.Exec("INSERT INTO accounts(id,nickname) VALUES ('00000000-0000-0000-0000-000000000001','合成')").Error; err != nil {
		t.Fatal(err)
	}
	if Run(ctx, db, log, "down") == nil {
		t.Fatal("有数据必须拒绝回退")
	}
	if err = db.Table("accounts").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("回退失败必须保留数据")
	}
	if err = db.Exec("UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1").Error; err != nil {
		t.Fatal(err)
	}
	if Run(ctx, db, log, "up") == nil {
		t.Fatal("文件篡改应拒绝")
	}
	// 在独立测试库中恢复账本，再验证空表回退和重新应用。
	if err = db.Exec("DELETE FROM accounts").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE public.schema_migrations SET checksum = ? WHERE version = 1", hex.EncodeToString(firstSum[:])).Error; err != nil {
		t.Fatal(err)
	}
	if err = Run(ctx, db, log, "down"); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"up", "down", "down", "up"} {
		if err = Run(ctx, db, log, direction); err != nil {
			t.Fatal(err)
		}
	}
	// T005/REQ-020：空表回退等待并发写入后必须重新观察数据，不能检查为空后静默删行。
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	if err = writer.Exec("INSERT INTO public.accounts(id,nickname) VALUES ('00000000-0000-0000-0000-000000000002','合成并发')").Error; err != nil {
		t.Fatal(err)
	}
	downResult := make(chan error, 1)
	downCtx, stopDown := context.WithTimeout(ctx, 5*time.Second)
	defer stopDown()
	go func() { downResult <- Run(downCtx, db, log, "down") }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting int64
		if err = db.Raw("SELECT count(*) FROM pg_locks WHERE relation = 'public.accounts'::regclass AND mode = 'AccessExclusiveLock' AND NOT granted").Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("回退未进入表锁等待")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err = <-downResult; err == nil {
		t.Fatal("并发写入完成后必须拒绝回退")
	}
	if err = db.Table("public.accounts").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("回退不得丢失并发提交的数据")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if Run(canceled, db, log, "up") == nil {
		t.Fatal("取消不能报告成功")
	}
	if observed.FilterField(zap.String("result", "failure")).Len() == 0 {
		t.Fatal("缺少失败完成日志")
	}
}
