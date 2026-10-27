package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// whisperSchemaDB 为每个升级场景创建独立数据库，不使用日常开发数据。
func whisperSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	parent := schemaDB(t, "APP_WHISPER_SCHEMA_TEST_DSN")
	name := "whisper_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := parent.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal("创建隔离测试数据库失败")
	}
	config := parent.Dialector.(*postgres.Dialector).Config
	// 使用同一个专用父库连接配置，只替换随机测试数据库名，不输出凭据。
	dsn := config.DSN
	var parentName string
	if e := parent.Raw("SELECT current_database()").Scan(&parentName).Error; e != nil {
		t.Fatal(e)
	}
	needle := "dbname=" + parentName + " "
	if !strings.Contains(dsn, needle) || (parentName != "liyuanci" && parentName != "whisper_test") {
		t.Fatal("需专用集成父库keyword DSN")
	}
	dsn = strings.Replace(dsn, needle, "dbname="+name+" ", 1)
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("连接隔离测试数据库失败")
	}
	pool, _ := db.DB()
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

// applyWhisperLegacy 建立真实历史SQL及原始校验账本，避免用新实体伪造旧库。
func applyWhisperLegacy(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("CREATE TABLE public.schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP)").Error; err != nil {
		t.Fatal(err)
	}
	entries, err := files.ReadDir("sql")
	if err != nil {
		t.Fatal(err)
	}
	version := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") || name >= "0021" {
			continue
		}
		up, _ := files.ReadFile("sql/" + name)
		down, _ := files.ReadFile("sql/" + strings.TrimSuffix(name, ".up.sql") + ".down.sql")
		sum := sha256.Sum256(append(append([]byte(nil), up...), down...))
		version++
		if err = db.Transaction(func(tx *gorm.DB) error {
			if e := tx.Exec(string(up)).Error; e != nil {
				return e
			}
			return tx.Exec("INSERT INTO public.schema_migrations(version,checksum) VALUES (?,?)", version, hex.EncodeToString(sum[:])).Error
		}); err != nil {
			t.Fatal("建立历史结构失败", name, err)
		}
	}
	if version != 20 {
		t.Fatal("历史迁移前缀不完整", version)
	}
}

// TestPostgresWhisperFoundation 验证REQ-009–011、014、017–020的数据基础，不计领域接口已实现。
func TestPostgresWhisperFoundation(t *testing.T) {
	ctx, log := context.Background(), zap.NewNop()
	t.Run("空库及迁移身份回退", func(t *testing.T) {
		db := whisperSchemaDB(t)
		for _, run := range []func() error{
			func() error { return Run(ctx, db, log, "up") },
			func() error { return ApplyPermissions(ctx, db, log) },
			func() error { return ApplyPermissions(ctx, db, log) },
		} {
			if err := run(); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Connection(func(conn *gorm.DB) error {
			if e := conn.Exec("SET ROLE liyuanci_migrator").Error; e != nil {
				return e
			}
			defer func() { _, _ = conn.Statement.ConnPool.ExecContext(ctx, "RESET ROLE") }()
			if e := Run(ctx, conn, log, "down"); e != nil {
				return e
			}
			return Run(ctx, conn, log, "up")
		}); err != nil {
			t.Fatal("迁移身份不能回退重建", err)
		}
		assertComments(t, db)
	})
	t.Run("旧库关系原位升级及约束权限", func(t *testing.T) {
		db := whisperSchemaDB(t)
		applyWhisperLegacy(t, db)
		must := func(sql string, args ...any) {
			t.Helper()
			if e := db.Exec(sql, args...).Error; e != nil {
				t.Fatal(e)
			}
		}
		reject := func(name, sql string, args ...any) {
			t.Helper()
			if e := db.Exec(sql, args...).Error; e == nil {
				t.Fatal("约束未拒绝", name)
			}
		}
		owner, peer := uuid.NewString(), uuid.NewString()
		must("INSERT INTO public.accounts(id,nickname) VALUES (?,'合成甲'),(?,'合成乙')", owner, peer)
		must("INSERT INTO public.blocks(owner_id,target_id,version) VALUES (?,?,7)", owner, peer)
		if err := Run(ctx, db, log, "up"); err != nil {
			t.Fatal(err)
		}
		var block struct {
			BlockID string
			Version int64
		}
		if err := db.Raw("SELECT block_id,version FROM public.blocks WHERE owner_id=?", owner).Scan(&block).Error; err != nil || block.Version != 7 || block.BlockID == owner || block.BlockID == peer || !validWhisperUUID(block.BlockID) {
			t.Fatal("旧关系或版本丢失", err)
		}
		if err := Run(ctx, db, log, "up"); err != nil {
			t.Fatal(err)
		}
		var same string
		if e := db.Raw("SELECT block_id FROM public.blocks WHERE owner_id=?", owner).Scan(&same).Error; e != nil || same != block.BlockID {
			t.Fatal("重跑改变opaque引用", e)
		}
		must("INSERT INTO public.blocks(owner_id,target_id) VALUES (?,?)", peer, owner)
		reject("重复opaque", "UPDATE public.blocks SET block_id=? WHERE owner_id=?", same, peer)
		must("INSERT INTO public.whisper_preferences(account_id) VALUES (?) ON CONFLICT DO NOTHING", owner)
		must("INSERT INTO public.whisper_preferences(account_id) VALUES (?) ON CONFLICT DO NOTHING", owner)
		var pref int64
		if e := db.Raw("SELECT version FROM public.whisper_preferences WHERE account_id=?", owner).Scan(&pref).Error; e != nil || pref != 1 {
			t.Fatal("首次昵称状态不是版本1", e)
		}
		post, content := uuid.NewString(), uuid.NewString()
		if e := db.Transaction(func(tx *gorm.DB) error {
			for _, q := range []struct {
				sql  string
				args []any
			}{
				{"INSERT INTO public.whisper_posts(id,owner_id) VALUES (?,?)", []any{post, owner}},
				{"INSERT INTO public.content_registry(id,kind,entity_id,author_id,content_version,school_detection_version) VALUES (?,'whisper_post',?,?,1,'pending')", []any{content, post, owner}},
				{"INSERT INTO public.whisper_post_versions(post_id,content_version,content_registry_id,body_cipher,identity_mode,identity_cipher) VALUES (?,1,?,'{}','anonymous','{}')", []any{post, content}},
			} {
				if e := tx.Exec(q.sql, q.args...).Error; e != nil {
					return e
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		reject("缺当前正文", "INSERT INTO public.whisper_posts(id,owner_id) VALUES (?,?)", uuid.NewString(), owner)
		reject("未审公开", "UPDATE public.whisper_post_versions SET public_body='合成正文',public_identity='{}' WHERE post_id=?", post)
		must("UPDATE public.whisper_post_versions SET review_state='approved',school_related=false,public_body=repeat('鲤',500),public_identity='{}' WHERE post_id=?", post)
		reject("公开正文501码点", "UPDATE public.whisper_post_versions SET public_body=repeat('鲤',501) WHERE post_id=?", post)
		reject("正文全空白", "UPDATE public.whisper_post_versions SET public_body=E' \\n\\t' WHERE post_id=?", post)
		reject("半匿名无字典", "UPDATE public.whisper_post_versions SET identity_mode='semi' WHERE post_id=?", post)
		reject("密文不是对象", "UPDATE public.whisper_post_versions SET body_cipher='[]' WHERE post_id=?", post)
		reject("裸公开指针", "UPDATE public.whisper_posts SET published_content_version=1 WHERE id=?", post)
		must("INSERT INTO public.whisper_aliases(post_id,actor_id,label) VALUES (?,?,'合成别名')", post, peer)
		reject("同帖重复映射", "INSERT INTO public.whisper_aliases(post_id,actor_id,label) VALUES (?,?,'其他别名')", post, peer)
		must("INSERT INTO public.whisper_hugs(post_id,actor_id) VALUES (?,?)", post, peer)
		reject("同人重复抱抱", "INSERT INTO public.whisper_hugs(post_id,actor_id) VALUES (?,?)", post, peer)
		must("UPDATE public.whisper_hugs SET active=false,version=2 WHERE post_id=? AND actor_id=?", post, peer)
		reject("零抱抱版本", "UPDATE public.whisper_hugs SET version=0 WHERE post_id=?", post)
		reject("未知来源", "INSERT INTO public.content_registry(id,kind,entity_id,author_id,content_version,school_detection_version) VALUES (?,'unknown',?,?,1,'pending')", uuid.NewString(), post, owner)
		for _, kind := range []string{"whisper_comment", "whisper_nickname", "whisper_capsule"} {
			must("INSERT INTO public.content_registry(id,kind,entity_id,author_id,content_version,school_detection_version) VALUES (?,?,?,?,1,'pending')", uuid.NewString(), kind, uuid.NewString(), owner)
		}
		// 未认证危机只保存明确状态，学号仍放在受控加密证据中；历史空值不编造身份。
		evidence, crisis := uuid.NewString(), uuid.NewString()
		must("INSERT INTO evidence.evidence_cases(id,kind,source_ref) VALUES (?,'crisis',?)", evidence, post)
		must("INSERT INTO evidence.crisis_cases(id,evidence_case_id,content_type,identity_status,source_entity_id,source_content_version) VALUES (?,?,'whisper_capsule','not_verified',?,1)", crisis, evidence, post)
		reject("虚构身份状态", "UPDATE evidence.crisis_cases SET identity_status='assumed' WHERE id=?", crisis)
		if e := ApplyPermissions(ctx, db, log); e != nil {
			t.Fatal(e)
		}
		assertComments(t, db)
		for _, tc := range []struct {
			role, statement string
			allowed         bool
		}{
			{"liyuanci_api", "SELECT body_cipher FROM public.whisper_capsules LIMIT 0", true},
			{"liyuanci_domain_worker", "SELECT body_cipher FROM public.whisper_capsules LIMIT 0", true},
			{"liyuanci_admin_api", "SELECT id,version FROM public.whisper_capsules LIMIT 0", true},
			{"liyuanci_admin_api", "SELECT body_cipher FROM public.whisper_capsules LIMIT 0", false},
			{"liyuanci_admin_api", "SELECT label_cipher FROM public.whisper_nickname_versions LIMIT 0", false},
			{"liyuanci_crisis_api", "SELECT body_cipher FROM public.whisper_capsules LIMIT 0", false},
			{"liyuanci_worker", "SELECT * FROM public.whisper_posts LIMIT 0", false},
			{"liyuanci_api", "DELETE FROM public.whisper_posts WHERE false", false},
			{"liyuanci_domain_worker", "UPDATE public.whisper_capsules SET body_cipher='{}' WHERE false", false},
			{"liyuanci_api", "CREATE TABLE public.whisper_runtime_ddl(id integer)", false},
			{"liyuanci_privacy_maintenance", "DELETE FROM public.whisper_aliases WHERE false", true},
		} {
			t.Run(fmt.Sprintf("权限_%s_%s", tc.role, tc.statement), func(t *testing.T) {
				e := underRole(db, tc.role, tc.statement)
				if (e == nil) != tc.allowed {
					t.Fatalf("权限预期%v，实际错误%v", tc.allowed, e)
				}
			})
		}
		if err := Run(ctx, db, log, "down"); err == nil {
			t.Fatal("非空必须拒绝回退")
		}
		if !db.Migrator().HasTable("public.whisper_posts") {
			t.Fatal("失败回退破坏数据结构")
		}
	})
}

func validWhisperUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }
