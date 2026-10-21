package migration

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func schemaDB(t *testing.T, env string) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skip("需专用 Docker 数据库；运行 python3 scripts/check-migrations.py")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("专用数据库连接失败")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func assertComments(t *testing.T, db *gorm.DB) {
	t.Helper()
	var missing int64
	err := db.Raw(`SELECT count(*) FROM (
 SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname IN ('public','evidence') AND c.relkind IN ('r','p') AND nullif(btrim(pg_catalog.obj_description(c.oid,'pg_class')),'') IS NULL
 UNION ALL
 SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
 WHERE n.nspname IN ('public','evidence') AND c.relkind IN ('r','p') AND a.attnum > 0 AND NOT a.attisdropped
 AND nullif(btrim(pg_catalog.col_description(c.oid,a.attnum)),'') IS NULL
) missing`).Scan(&missing).Error
	if err != nil || missing != 0 {
		t.Fatalf("表或字段缺少数据库注释：%d，%v", missing, err)
	}
}

func underRole(db *gorm.DB, role, statement string) error {
	// role 仅来自测试中的固定白名单，不接收外部输入。
	return db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SET ROLE " + role).Error; err != nil {
			return err
		}
		// 即使 GORM 句柄保留失败状态，也必须直接复位底层连接，避免角色污染连接池。
		defer func() { _, _ = conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE") }()
		return conn.Exec(statement).Error
	})
}

func TestBusinessSchema(t *testing.T) {
	// T005/REQ-002–008、014–020：验证数据库存储不变量，不冒称对应领域服务已完成。
	db := schemaDB(t, "APP_SCHEMA_TEST_DSN")
	ctx := context.Background()
	log := zap.NewNop()
	if err := Run(ctx, db, log, "up"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	// 专用迁移角色须确实能够回退和重建 schema，不依赖测试超级用户代执行。
	if err := db.Connection(func(conn *gorm.DB) error {
		if e := conn.Exec("SET ROLE liyuanci_migrator").Error; e != nil {
			return e
		}
		defer func() { _, _ = conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE") }()
		if e := Run(ctx, conn, log, "down"); e != nil {
			return e
		}
		return Run(ctx, conn, log, "up")
	}); err != nil {
		t.Fatal("迁移权限组不能完成空库回退及重建", err)
	}
	assertComments(t, db)
	required := []string{"identity_names", "admin_accounts", "admin_role_bindings", "sessions", "assets", "identity_reviews", "blocks", "penalties", "appeals", "wishes", "wish_versions", "applications", "matches", "conversations", "messages", "story_versions", "content_registry", "wordlist_versions", "wordlist_entries", "moderation_tasks", "reports", "content_restrictions", "privacy_holds", "work_items", "outbox_events", "consumer_receipts", "notifications", "delivery_attempts", "command_receipts", "access_audits", "deletion_jobs"}
	for _, table := range required {
		if !db.Migrator().HasTable("public." + table) {
			t.Fatal("设计表缺失", table)
		}
	}
	for _, table := range []string{"evidence_cases", "crisis_cases", "assets", "evidence_items", "evidence_actions"} {
		if !db.Migrator().HasTable("evidence." + table) {
			t.Fatal("证据设计表缺失", table)
		}
	}
	must := func(sql string, args ...any) {
		t.Helper()
		if err := db.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	reject := func(name, sql string, args ...any) {
		t.Helper()
		if err := db.Exec(sql, args...).Error; err == nil {
			t.Fatal("非法数据未被拒绝", name)
		}
	}
	uid := func(n int) string { return fmt.Sprintf("30000000-0000-0000-0000-%012d", n) }
	for i := 1; i <= 4; i++ {
		must("INSERT INTO public.accounts(id,nickname) VALUES (?,?)", uid(i), "合成账号")
	}
	admin := uid(10)
	must("INSERT INTO public.admin_accounts(id,credential_ref) VALUES (?,?)", admin, "synthetic-provider-reference")
	reject("后台无凭据", "INSERT INTO public.admin_accounts(id) VALUES (?)", uid(11))
	reject("学生后台主体混用", "INSERT INTO public.sessions(id,account_id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,decode(repeat('ab',32),'hex'),1,CURRENT_TIMESTAMP+interval '1 hour')", uid(12), uid(1), admin)
	must("INSERT INTO public.assets(id,owner_id,purpose,encrypted_object_key,object_version,wrapped_key_ref) VALUES (?,?,'student_card','{}','synthetic-version','synthetic-key')", uid(13), uid(1))
	must("INSERT INTO public.identity_reviews(id,account_id,asset_id) VALUES (?,?,?)", uid(14), uid(1), uid(13))
	reject("重复待审身份", "INSERT INTO public.identity_reviews(id,account_id,asset_id,version) VALUES (?,?,?,2)", uid(15), uid(1), uid(13))
	reject("驳回缺少原因", "UPDATE public.identity_reviews SET status='rejected' WHERE id=?", uid(14))
	for _, duration := range []int{0, 31} {
		reject("期限越界", "INSERT INTO public.wishes(id,owner_id,current_content_version,category,campus,duration_days) VALUES (?,?,1,'help','wushan',?)", uid(20+duration), uid(1), duration)
	}
	reject("未知分类", "INSERT INTO public.wishes(id,owner_id,current_content_version,category,campus) VALUES (?,?,1,'invalid','wushan')", uid(21), uid(1))
	wish := uid(30)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("INSERT INTO public.wishes(id,owner_id,current_content_version,category,campus,duration_days) VALUES (?,?,1,'team','any',30)", wish, uid(1)).Error; e != nil {
			return e
		}
		return tx.Exec("INSERT INTO public.wish_versions(wish_id,content_version,body_cipher,school_detection_version) VALUES (?,1,'{}','synthetic-detector')", wish).Error
	}); err != nil {
		t.Fatal(err)
	}
	reject("缺失目标内容版本", "INSERT INTO public.wishes(id,owner_id,current_content_version,category,campus) VALUES (?,?,1,'help','wushan')", uid(31), uid(1))
	reject("未检测不能默认为不涉校", "UPDATE public.wish_versions SET school_related=false WHERE wish_id=?", wish)
	reject("未审核公开正文", "UPDATE public.wish_versions SET public_body='合成正文' WHERE wish_id=?", wish)
	must("UPDATE public.wish_versions SET moderation_status='approved',school_related=false,school_detection_state='completed',public_body=repeat('鲤',500) WHERE wish_id=?", wish)
	reject("公开投影超过五百码点", "UPDATE public.wish_versions SET public_body=repeat('鲤',501) WHERE wish_id=?", wish)
	must("INSERT INTO public.applications(id,wish_id,wish_content_version,applicant_id,message_cipher) VALUES (?,?,1,?,'{}')", uid(40), wish, uid(2))
	reject("重复待审应愿", "INSERT INTO public.applications(id,wish_id,wish_content_version,applicant_id,message_cipher) VALUES (?,?,1,?,'{}')", uid(41), wish, uid(2))
	must("UPDATE public.applications SET status='withdrawn' WHERE id=?", uid(40))
	must("INSERT INTO public.applications(id,wish_id,wish_content_version,applicant_id,message_cipher) VALUES (?,?,1,?,'{}')", uid(41), wish, uid(2))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- db.Exec("INSERT INTO public.matches(id,wish_id,requester_id,helper_id) VALUES (?,?,?,?)", uid(50+i), wish, uid(1), uid(2+i)).Error
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("同心愿并发只能一个当前匹配", successes)
	}
	reject("匹配许愿人不符", "INSERT INTO public.matches(id,wish_id,requester_id,helper_id,status,ended_at) VALUES (?,?,?,?,'ended',CURRENT_TIMESTAMP)", uid(55), wish, uid(3), uid(2))
	var match string
	if err := db.Raw("SELECT id FROM public.matches WHERE wish_id=?", wish).Scan(&match).Error; err != nil {
		t.Fatal(err)
	}
	conversation := uid(60)
	must("INSERT INTO public.conversations(id,match_id) VALUES (?,?)", conversation, match)
	reject("待审占序号", "INSERT INTO public.messages(id,conversation_id,sender_id,delivery_seq,client_message_id,body_cipher,cancel_epoch) VALUES (?,?,?,1,?,'{}',1)", uid(61), conversation, uid(1), uid(62))
	reject("送达无序号", "INSERT INTO public.messages(id,conversation_id,sender_id,status,client_message_id,body_cipher,cancel_epoch) VALUES (?,?,?,'delivered',?,'{}',1)", uid(61), conversation, uid(1), uid(62))
	// 只验证计数器可随事务回滚，不模拟未实现的业务审核/消息消费者。
	err := db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("UPDATE public.conversations SET next_delivery_seq=next_delivery_seq+1 WHERE id=?", conversation).Error; e != nil {
			return e
		}
		return tx.Exec("INSERT INTO public.messages(id,conversation_id,sender_id,client_message_id,body_cipher,cancel_epoch) VALUES (?,?,?,?,'{}',1)", uid(61), conversation, uid(999), uid(62)).Error
	})
	if err == nil {
		t.Fatal("失效发送者应回滚")
	}
	var next int64
	if err = db.Raw("SELECT next_delivery_seq FROM public.conversations WHERE id=?", conversation).Scan(&next).Error; err != nil || next != 1 {
		t.Fatal("回滚消耗送达计数器", next, err)
	}
	must("INSERT INTO public.messages(id,conversation_id,sender_id,status,delivery_seq,delivered_at,client_message_id,body_cipher,cancel_epoch) VALUES (?,?,?,'delivered',1,CURRENT_TIMESTAMP,?,'{}',1)", uid(61), conversation, uid(1), uid(62))
	reject("送达序号重复", "INSERT INTO public.messages(id,conversation_id,sender_id,status,delivery_seq,delivered_at,client_message_id,body_cipher,cancel_epoch) VALUES (?,?,?,'delivered',1,CURRENT_TIMESTAMP,?,'{}',1)", uid(63), conversation, uid(1), uid(64))
	must("INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version) VALUES (?,'story',?,1,2)", uid(70), wish)
	reject("重复有效屏障", "INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version) VALUES (?,'story',?,2,3)", uid(71), wish)
	must("INSERT INTO public.penalties(id,account_id,level,action_kind,violation_ref,content_version,starts_at,reason_ref,first_displayed_at,confirmable_at) VALUES (?,?,'minor','warning',?,1,CURRENT_TIMESTAMP,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+interval '30 seconds')", uid(80), uid(2), uid(81), uid(82))
	reject("二十九秒确认", "UPDATE public.penalties SET acknowledged_at=first_displayed_at+interval '29 seconds' WHERE id=?", uid(80))
	must("UPDATE public.penalties SET acknowledged_at=confirmable_at WHERE id=?", uid(80))
	reject("禁言缺期限", "INSERT INTO public.penalties(id,account_id,level,action_kind,violation_ref,content_version,starts_at,reason_ref) VALUES (?,?,'general','mute',?,1,CURRENT_TIMESTAMP,?)", uid(83), uid(2), uid(84), uid(85))
	reject("禁言任意时长", "INSERT INTO public.penalties(id,account_id,level,action_kind,violation_ref,content_version,starts_at,ends_at,reason_ref) VALUES (?,?,'general','mute',?,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+interval '1 hour',?)", uid(83), uid(2), uid(84), uid(85))
	must("INSERT INTO public.work_items(id,kind,target_kind,target_id,operation_id) VALUES (?,'synthetic','wish',?,?)", uid(90), wish, uid(91))
	reject("工作操作重复", "INSERT INTO public.work_items(id,kind,target_kind,target_id,operation_id) VALUES (?,'synthetic','wish',?,?)", uid(92), wish, uid(91))
	must("INSERT INTO public.consumer_receipts(consumer_kind,operation_id) VALUES ('synthetic',?)", uid(91))
	reject("消费回执重复", "INSERT INTO public.consumer_receipts(consumer_kind,operation_id) VALUES ('synthetic',?)", uid(91))
	reject("完成删除仍留身份", "INSERT INTO public.deletion_jobs(job_id,subject_ref,phase) VALUES (?,?,'completed')", uid(93), uid(2))
	caseID := uid(100)
	must("INSERT INTO evidence.evidence_cases(id,kind,source_ref) VALUES (?,'crisis',?)", caseID, uid(101))
	must("INSERT INTO evidence.evidence_items(id,case_id,subject_cipher,body_cipher,key_ref) VALUES (?,?,'{}','{}','synthetic-key')", uid(102), caseID)
	must("DELETE FROM public.accounts WHERE id=?", uid(4))
	var evidenceCount int64
	if err = db.Table("evidence.evidence_items").Count(&evidenceCount).Error; err != nil || evidenceCount != 1 {
		t.Fatal("普通账号清理不得级联证据")
	}
	if Run(ctx, db, log, "down") == nil {
		t.Fatal("存在业务数据必须拒绝整批回退")
	}
	if !db.Migrator().HasTable("public.messages") {
		t.Fatal("失败回退不能移除其他空表")
	}
	if err = ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	must("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?, ?, decode(repeat('01',32),'hex'), 1, CURRENT_TIMESTAMP+interval '1 hour')", uid(110), uid(1))
	must("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?, ?, decode(repeat('02',32),'hex'), 1, CURRENT_TIMESTAMP+interval '1 hour')", uid(111), admin)
	for _, check := range []struct {
		role, sql string
		allowed   bool
	}{
		{"liyuanci_api", "SELECT id FROM public.wishes", true},
		{"liyuanci_api", "SELECT id FROM evidence.evidence_cases", false},
		{"liyuanci_admin_api", "SELECT id FROM evidence.evidence_cases", false},
		{"liyuanci_worker", "SELECT id FROM public.messages", false},
		{"liyuanci_domain_worker", "SELECT id FROM public.messages", true},
		{"liyuanci_domain_worker", "SELECT account_id FROM public.identity_names", false},
		{"liyuanci_evidence_reader", "SELECT id FROM evidence.evidence_items", true},
		{"liyuanci_evidence_reader", "DELETE FROM evidence.evidence_items WHERE false", false},
		{"liyuanci_evidence_writer", "DELETE FROM evidence.evidence_items WHERE false", false},
		{"liyuanci_evidence_purger", "DELETE FROM evidence.evidence_items WHERE false", true},
		{"liyuanci_api", "UPDATE public.admin_role_bindings SET role=role WHERE false", false},
		{"liyuanci_admin_api", "UPDATE public.admin_role_bindings SET role=role WHERE false", false},
		{"liyuanci_admin_maintenance", "UPDATE public.admin_role_bindings SET role=role WHERE false", true},
		{"liyuanci_api", "CREATE TABLE public.forbidden_object(id uuid)", false},
		{"liyuanci_api", "DO $$ BEGIN IF EXISTS (SELECT 1 FROM public.sessions WHERE admin_id IS NOT NULL) THEN RAISE EXCEPTION '后台会话泄漏'; END IF; END $$", true},
		{"liyuanci_admin_api", "DO $$ BEGIN IF EXISTS (SELECT 1 FROM public.sessions WHERE account_id IS NOT NULL) THEN RAISE EXCEPTION '学生会话泄漏'; END IF; END $$", true},
		{"liyuanci_api", "INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES ('30000000-0000-0000-0000-000000000112','30000000-0000-0000-0000-000000000010',decode(repeat('03',32),'hex'),1,CURRENT_TIMESTAMP+interval '1 hour')", false},
		{"liyuanci_admin_api", "INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES ('30000000-0000-0000-0000-000000000113','30000000-0000-0000-0000-000000000001',decode(repeat('04',32),'hex'),1,CURRENT_TIMESTAMP+interval '1 hour')", false},
	} {
		e := underRole(db, check.role, check.sql)
		if (e == nil) != check.allowed {
			t.Fatalf("实际角色权限不符：%s，%s，%v", check.role, check.sql, e)
		}
	}
	// 模拟旧版本误授权，显式重放矩阵必须移除，而不是只追加新权限。
	must("GRANT USAGE ON SCHEMA evidence TO liyuanci_api")
	must("GRANT SELECT ON evidence.evidence_items TO liyuanci_api")
	must("DO $$ BEGIN EXECUTE format('GRANT CREATE ON DATABASE %I TO liyuanci_api', current_database()); END $$")
	if err = ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	if underRole(db, "liyuanci_api", "SELECT id FROM evidence.evidence_items") == nil {
		t.Fatal("历史越权授权未撤销")
	}
	if underRole(db, "liyuanci_api", "CREATE SCHEMA forbidden_schema") == nil {
		t.Fatal("历史数据库建 schema 权限未撤销")
	}
	// T005/REQ-018、019：权限重放必须恢复被关闭的 RLS，未知策略则失败且不擅自覆盖。
	must("ALTER TABLE public.sessions DISABLE ROW LEVEL SECURITY")
	if err = ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	if underRole(db, "liyuanci_api", "DO $$ BEGIN IF EXISTS (SELECT 1 FROM public.sessions WHERE admin_id IS NOT NULL) THEN RAISE EXCEPTION '后台会话泄漏'; END IF; END $$") != nil {
		t.Fatal("权限重放未恢复会话行级隔离")
	}
	must("CREATE POLICY unmanaged_scope ON public.sessions TO PUBLIC USING (true)")
	if ApplyPermissions(ctx, db, log) == nil {
		t.Fatal("未知策略必须拒绝而非保留或静默删除")
	}
	must("DROP POLICY unmanaged_scope ON public.sessions")
	must("ALTER SCHEMA public OWNER TO liyuanci_api")
	if ApplyPermissions(ctx, db, log) == nil {
		t.Fatal("运行组拥有公共命名空间时必须拒绝")
	}
	must("ALTER SCHEMA public OWNER TO pg_database_owner")
	if err = ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal("修复现场配置后应可重新应用", err)
	}
	if RunJournal(ctx, db, log, "up") == nil {
		t.Fatal("业务库不得同时部署恢复日志")
	}
}

func TestRecoverySchema(t *testing.T) {
	// T005/REQ-018、020：独立逻辑库结构测试，不代表独立物理持久化或恢复验收。
	db := schemaDB(t, "APP_RECOVERY_TEST_DSN")
	ctx := context.Background()
	core, observed := observer.New(zap.InfoLevel)
	log := zap.New(core)
	for _, action := range []string{"up", "up", "permissions", "permissions"} {
		if err := RunJournal(ctx, db, log, action); err != nil {
			t.Fatal(err)
		}
	}
	assertComments(t, db)
	if Run(ctx, db, log, "up") == nil {
		t.Fatal("恢复库不能混入业务结构")
	}
	if err := db.Exec("ALTER SCHEMA public OWNER TO liyuanci_recovery_writer").Error; err != nil {
		t.Fatal(err)
	}
	if RunJournal(ctx, db, log, "permissions") == nil {
		t.Fatal("恢复运行组拥有命名空间时必须拒绝")
	}
	if err := db.Exec("ALTER SCHEMA public OWNER TO pg_database_owner").Error; err != nil {
		t.Fatal(err)
	}
	if err := RunJournal(ctx, db, log, "permissions"); err != nil {
		t.Fatal(err)
	}
	if underRole(db, "liyuanci_recovery_writer", "INSERT INTO public.recovery_delete_journal(intent_id,deletion_scope,entry_envelope) VALUES ('40000000-0000-0000-0000-000000000001','account','{}')") != nil {
		t.Fatal("写入角色应能记录意图")
	}
	if RunJournal(ctx, db, log, "down") == nil {
		t.Fatal("非空恢复日志必须拒绝回退")
	}
	if underRole(db, "liyuanci_recovery_writer", "DELETE FROM public.recovery_delete_journal") == nil {
		t.Fatal("写入角色不能清除意图")
	}
	if underRole(db, "liyuanci_recovery_writer", "UPDATE public.recovery_delete_journal SET entry_envelope='{}'") == nil {
		t.Fatal("写入角色不能改写已有意图")
	}
	if underRole(db, "liyuanci_recovery_writer", "UPDATE public.recovery_delete_journal SET confirmed_at=CURRENT_TIMESTAMP") != nil {
		t.Fatal("写入角色应能确认意图")
	}
	if underRole(db, "liyuanci_recovery_reader", "SELECT * FROM public.recovery_delete_journal") != nil {
		t.Fatal("恢复读取角色应可读意图")
	}
	if underRole(db, "liyuanci_api", "SELECT * FROM public.recovery_delete_journal") == nil {
		t.Fatal("业务角色不能读恢复日志")
	}
	if err := db.Exec("GRANT SELECT ON public.recovery_delete_journal TO liyuanci_api").Error; err != nil {
		t.Fatal(err)
	}
	if err := RunJournal(ctx, db, log, "permissions"); err != nil {
		t.Fatal(err)
	}
	if underRole(db, "liyuanci_api", "SELECT * FROM public.recovery_delete_journal") == nil {
		t.Fatal("重放恢复权限矩阵未移除历史误授权")
	}
	if underRole(db, "liyuanci_recovery_retention", "DELETE FROM public.recovery_delete_journal") != nil {
		t.Fatal("显式保留期维护角色应可删除已验收记录")
	}
	for _, action := range []string{"down", "down", "up"} {
		if err := RunJournal(ctx, db, log, action); err != nil {
			t.Fatal(err)
		}
	}
	if observed.FilterField(zap.String("result", "failure")).Len() == 0 || observed.FilterField(zap.String("result", "success")).Len() == 0 {
		t.Fatal("恢复日志结构缺少成功或失败完成记录")
	}
	for _, entry := range observed.FilterMessage("恢复日志结构操作完成").All() {
		if entry.ContextMap()["duration"] == nil || entry.ContextMap()["operation"] == nil {
			t.Fatal("恢复结构完成日志缺少操作或耗时")
		}
	}
}
