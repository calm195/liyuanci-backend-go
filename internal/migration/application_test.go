package migration

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// applicationSchemaDB 在本轮临时集群创建普通库所有者，验证真实单账号权限而非超级用户旁路。
func applicationSchemaDB(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	admin := whisperSchemaDB(t)
	name := "schema_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := uuid.NewString()
	var database string
	if err := admin.Raw("SELECT current_database()").Scan(&database).Error; err != nil {
		t.Fatal(err)
	}
	if err := admin.Exec("CREATE ROLE " + name + " LOGIN PASSWORD '" + password + "'").Error; err != nil {
		t.Fatal("创建测试库所有者失败")
	}
	if err := admin.Exec("ALTER DATABASE " + database + " OWNER TO " + name).Error; err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(admin.Dialector.(*postgres.Dialector).Config.DSN)
	for i, p := range parts {
		if strings.HasPrefix(p, "user=") {
			parts[i] = "user=" + name
		}
		if strings.HasPrefix(p, "password=") {
			parts[i] = "password=" + password
		}
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: strings.Join(parts, " "), PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("测试应用数据库连接失败")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return admin, db
}

// schemaSnapshot 覆盖列、约束、索引、函数、行策略与RLS状态，避免重跑造成无版本结构变更。
func schemaSnapshot(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var rows []string
	if err := db.Raw(`SELECT value FROM (
 SELECT 'column:'||n.nspname||'.'||c.relname||'.'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') AS value
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
 WHERE n.nspname IN ('public','evidence') AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL SELECT 'constraint:'||n.nspname||'.'||c.conname||':'||pg_get_constraintdef(c.oid)
 FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname IN ('public','evidence')
 UNION ALL SELECT 'index:'||schemaname||'.'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname IN ('public','evidence')
 UNION ALL SELECT 'function:'||pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname IN ('public','evidence')
 UNION ALL SELECT 'policy:'||schemaname||'.'||tablename||'.'||policyname||':'||roles::text||':'||coalesce(qual,'')||':'||coalesce(with_check,'') FROM pg_policies WHERE schemaname IN ('public','evidence')
 UNION ALL SELECT 'rls:'||n.nspname||'.'||c.relname||':'||c.relrowsecurity||':'||c.relforcerowsecurity
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','evidence') AND c.relkind='r'
 ) definitions ORDER BY value`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestPostgresApplicationMigrations 覆盖权限失败原子回滚、SQL独占结构、非空和并发回退保护。
func TestPostgresApplicationMigrations(t *testing.T) {
	admin, db := applicationSchemaDB(t)
	ctx, log := context.Background(), zap.NewNop()
	// 超级用户会在最后权限步骤被拒绝，前面的DDL及账本必须一起消失。
	if err := RunApplication(ctx, admin, log, "up"); err == nil {
		t.Fatal("超级用户不能成为应用账号")
	}
	for _, table := range []string{"public.accounts", "public.schema_migrations", "evidence.evidence_cases"} {
		if admin.Migrator().HasTable(table) {
			t.Fatalf("权限失败残留表：%s", table)
		}
	}
	if err := RunApplication(ctx, db, log, "up"); err != nil {
		t.Fatal(err)
	}
	assertComments(t, db)
	var definitions []struct {
		TableName, ColumnName, IsNullable string
		ColumnDefault                     *string
	}
	if err := db.Raw(`SELECT table_name,column_name,is_nullable,column_default FROM information_schema.columns
 WHERE (table_schema='public' AND table_name='share_links' AND column_name='created_at')
 OR (table_schema='evidence' AND table_name='crisis_deliveries' AND column_name='attempts')`).Scan(&definitions).Error; err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatal("缺少关键字段")
	}
	for _, column := range definitions {
		if column.IsNullable != "NO" || column.ColumnDefault == nil {
			t.Fatalf("缺少非空或默认值：%s.%s", column.TableName, column.ColumnName)
		}
	}
	before := schemaSnapshot(t, db)
	for _, run := range []func() error{
		func() error { return RunApplication(ctx, db, log, "up") },
		func() error { return ApplyApplicationPermissions(ctx, db, log) },
	} {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, schemaSnapshot(t, db)) {
		t.Fatal("重跑修改了结构或函数定义")
	}
	// 额外行策略不能静默保留或覆盖。
	if err := db.Exec("CREATE POLICY unknown_scope ON public.resource_categories USING(true)").Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyApplicationPermissions(ctx, db, log); err == nil {
		t.Fatal("未登记策略应被拒绝")
	}
	if err := db.Exec("DROP POLICY unknown_scope ON public.resource_categories").Error; err != nil {
		t.Fatal(err)
	}
	// 不依赖学生或后台账号即可存在的资料，必须受到整体回退保护。
	category := uuid.NewString()
	insert := func(conn *gorm.DB) error {
		return conn.Exec(`INSERT INTO public.resource_categories(id,name,name_key,sort_order,enabled,version,created_at,updated_at)
 VALUES (?,'合成分类','synthetic',0,true,1,clock_timestamp(),clock_timestamp())`, category).Error
	}
	if err := insert(db); err != nil {
		t.Fatal(err)
	}
	// 取消当前账号策略，使RLS把真实记录隐藏，验证保护不会误判为空。
	if err := db.Exec("DROP POLICY application_account ON public.resource_categories").Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("public.resource_categories").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("未建立RLS隐藏场景", err)
	}
	if err := RunApplication(ctx, db, log, "down"); err == nil || !strings.Contains(err.Error(), "resource_categories") {
		t.Fatal("非空资料回退应被拒绝", err)
	}
	var forced bool
	if err := db.Raw("SELECT relforcerowsecurity FROM pg_class WHERE oid='public.resource_categories'::regclass").
		Scan(&forced).
		Error; err != nil || !forced {
		t.Fatal("失败回退未恢复RLS", err)
	}
	if err := ApplyApplicationPermissions(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	if err := db.Table("public.resource_categories").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("回退丢失资料", err)
	}
	if err := db.Exec("DELETE FROM public.resource_categories").Error; err != nil {
		t.Fatal(err)
	}
	// 回退等待资料写事务后，必须读取其已提交记录并拒绝删除。
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	if err := insert(writer); err != nil {
		t.Fatal(err)
	}
	downCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- RunApplication(downCtx, db, log, "down") }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting int64
		if err := admin.Raw("SELECT count(*) FROM pg_locks WHERE relation='public.resource_categories'::regclass AND mode='AccessExclusiveLock' AND NOT granted").
			Scan(&waiting).
			Error; err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("资料回退未等待并发写入")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("回退不能删除并发提交的资料")
	}
	if err := db.Table("public.resource_categories").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("并发资料丢失", err)
	}
	if err := db.Exec("DELETE FROM public.resource_categories").Error; err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"down", "down", "up"} {
		if err := RunApplication(ctx, db, log, direction); err != nil {
			t.Fatal(err)
		}
	}
	// 实体只是映射，缺字段时失败，不再偷偷补列。
	if err := db.Exec("ALTER TABLE public.accounts DROP COLUMN hide_identity_tags").Error; err != nil {
		t.Fatal(err)
	}
	if err := RunApplication(ctx, db, log, "up"); err == nil {
		t.Fatal("结构漂移必须报错")
	}
	if db.Migrator().HasColumn("public.accounts", "hide_identity_tags") {
		t.Fatal("结构漂移被静默修改")
	}
}
