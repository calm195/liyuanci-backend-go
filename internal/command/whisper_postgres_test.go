package command

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

// TestPostgresWhisperLocks 验证四类资源真实行锁、昵称随机引用及评论双并发字段，非业务接口验收。
func TestPostgresWhisperLocks(t *testing.T) {
	dsn := os.Getenv("APP_WHISPER_COMMAND_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PostgreSQL；make integration覆盖此用例")
	}
	open := func(dsn string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if e != nil {
			t.Fatal("隔离数据库连接失败")
		}
		pool, _ := db.DB()
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	parent := open(dsn)
	var parentName string
	if e := parent.Raw("SELECT current_database()").Scan(&parentName).Error; e != nil {
		t.Fatal(e)
	}
	needle := "dbname=" + parentName + " "
	if !strings.Contains(dsn, needle) || (parentName != "liyuanci" && parentName != "whisper_test") {
		t.Fatal("需专用集成父库keyword DSN")
	}
	name := "whisper_locks_" + strings.ReplaceAll(googleuuid.NewString(), "-", "")
	if e := parent.Exec("CREATE DATABASE " + name).Error; e != nil {
		t.Fatal("创建隔离数据库失败")
	}
	db := open(strings.Replace(dsn, needle, "dbname="+name+" ", 1))
	ctx, log := context.Background(), zap.NewNop()
	if e := migration.Run(ctx, db, log, "up"); e != nil {
		t.Fatal(e)
	}
	if e := migration.ApplyPermissions(ctx, db, log); e != nil {
		t.Fatal(e)
	}
	owner, post, comment, capsule, nickname := googleuuid.NewString(), googleuuid.NewString(), googleuuid.NewString(), googleuuid.NewString(), googleuuid.NewString()
	postContent, commentContent, capsuleContent := googleuuid.NewString(), googleuuid.NewString(), googleuuid.NewString()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(db.Exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'合成主体','verified')", owner).Error)
	must(db.Transaction(func(tx *gorm.DB) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO public.content_registry(id,kind,entity_id,author_id,content_version,school_detection_version) VALUES (?,'whisper_post',?,?,1,'pending'),(?,'whisper_comment',?,?,1,'pending'),(?,'whisper_capsule',?,?,1,'pending'),(?,'whisper_nickname',?,?,2,'pending')", []any{postContent, post, owner, commentContent, comment, owner, capsuleContent, capsule, owner, nickname, nickname, owner}},
			{"INSERT INTO public.whisper_posts(id,owner_id,version,comments_epoch) VALUES (?,?,3,8)", []any{post, owner}},
			{"INSERT INTO public.whisper_post_versions(post_id,content_version,content_registry_id,body_cipher,identity_mode,identity_cipher) VALUES (?,1,?,'{}','anonymous','{}')", []any{post, postContent}},
			{"INSERT INTO public.whisper_comments(id,post_id,author_id,version,comments_epoch) VALUES (?,?,?,4,8)", []any{comment, post, owner}},
			{"INSERT INTO public.whisper_comment_versions(comment_id,content_version,content_registry_id,body_cipher,identity_mode,identity_cipher) VALUES (?,1,?,'{}','anonymous','{}')", []any{comment, commentContent}},
			{"INSERT INTO public.whisper_capsules(id,owner_id,content_registry_id,body_cipher,unlock_at,version) VALUES (?,?,?,'{}',date_trunc('second',CURRENT_TIMESTAMP)+interval '1 day',5)", []any{capsule, owner, capsuleContent}},
			{"INSERT INTO public.whisper_nickname_versions(account_id,nickname_version,content_registry_id,label_cipher,control_version) VALUES (?,2,?,'{}',6)", []any{owner, nickname}},
		} {
			if e := tx.Exec(q.sql, q.args...).Error; e != nil {
				return e
			}
		}
		return nil
	}))
	plan := Discovery{Accounts: []string{owner}, Resources: []Resource{{Kind: WhisperComment, ID: comment}, {Kind: WhisperCapsule, ID: capsule}, {Kind: WhisperNickname, ID: nickname}, {Kind: WhisperPost, ID: post}}}
	for _, role := range []string{"liyuanci_domain_worker", "liyuanci_admin_api"} {
		t.Run(role, func(t *testing.T) {
			must(db.Connection(func(conn *gorm.DB) error {
				if e := conn.Exec("SET ROLE " + role).Error; e != nil {
					return e
				}
				defer func() { _, _ = conn.Statement.ConnPool.ExecContext(ctx, "RESET ROLE") }()
				return conn.Transaction(func(txDB *gorm.DB) error {
					tx, e := LockPlan(ctx, txDB, plan, time.Second)
					if e != nil {
						return e
					}
					for i, ref := range plan.Resources {
						expected := []int64{4, 5, 6, 3}[i]
						got, ok := tx.Resource(ref)
						if !ok || got.Version != expected {
							return fmt.Errorf("资源版本错误：%s", ref.Kind)
						}
						if ref.Kind == WhisperPost && got.CommentsEpoch != 8 {
							return fmt.Errorf("父帖代次错误")
						}
					}
					return nil
				})
			}))
		})
	}
	// 一个持锁事务尚未提交时，第二个事务不能越过父帖行锁。
	writer := db.Begin()
	must(writer.Error)
	defer writer.Rollback()
	_, e := LockPlan(ctx, writer, plan, time.Second)
	must(e)
	e = db.Transaction(func(tx *gorm.DB) error {
		_, err := LockPlan(ctx, tx, Discovery{Resources: []Resource{{Kind: WhisperPost, ID: post}}}, 30*time.Millisecond)
		return err
	})
	if e == nil {
		t.Fatal("并发事务越过了已持有的父帖锁")
	}
	must(writer.Rollback().Error)
	token := googleuuid.NewString()
	hash := sha256.Sum256([]byte(token))
	must(db.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,CURRENT_TIMESTAMP+interval '1 hour')", googleuuid.NewString(), owner, hash[:]).Error)
	d := Definition{Kind: "whisper.foundation_check", Method: "PUT", ResourcePath: "/synthetic/whisper-foundation", ActorKind: "student", Access: Ordinary, RequiredExpectations: []string{"expected_version", "expected_post_version", "expected_comments_epoch"}, Discover: func(context.Context, *gorm.DB, Subject) (Discovery, error) { return plan, nil }, Authorize: func(context.Context, *Tx) error { return nil }, Apply: func(_ context.Context, tx *Tx) (string, error) {
		return comment, tx.DB().Exec("UPDATE public.whisper_comments SET version=version+1 WHERE id=?", comment).Error
	}, Resolve: func(_ context.Context, _ *Tx, ref string) error {
		if ref != comment {
			return ErrNotFound
		}
		return nil
	}}
	engine, e := New(db, log, config())
	must(e)
	input := Input{Token: token, IdempotencyKey: googleuuid.NewString(), Request: struct{}{}, Expectations: []Expectation{{Name: "expected_version", Target: Resource{Kind: WhisperComment, ID: comment}, Value: 4}, {Name: "expected_post_version", Target: Resource{Kind: WhisperPost, ID: post}, Value: 3}, {Name: "expected_comments_epoch", Target: Resource{Kind: WhisperPost, ID: post}, Value: 7}}}
	if _, e = engine.Execute(ctx, d, input); e != ErrVersionConflict {
		t.Fatal("旧评论代次未拒绝", e)
	}
	input.Expectations[2].Value = 8
	if _, e = engine.Execute(ctx, d, input); e != nil {
		t.Fatal("当前版本无法提交", e)
	}
	out, e := engine.Execute(ctx, d, input)
	if e != nil || !out.Replayed {
		t.Fatal("成功回放再次校验旧版本", e)
	}
	input.IdempotencyKey = googleuuid.NewString()
	if _, e = engine.Execute(ctx, d, input); e != ErrVersionConflict {
		t.Fatal("新命令未拒绝旧资源版本", e)
	}
	var actual int64
	must(db.Raw("SELECT version FROM public.whisper_comments WHERE id=?", comment).Scan(&actual).Error)
	if actual != 5 {
		t.Fatal("版本冲突或幂等回放发生重复写入", actual)
	}
}
