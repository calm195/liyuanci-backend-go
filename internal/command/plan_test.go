package command

import (
	"context"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

// T006/T007/REQ-014、016、018、020：后台工作复用锁序，不把取得账号 guard 等同于账号写权限。
func TestBackgroundPlan(t *testing.T) {
	if _, err := LockPlan(context.Background(), nil, Discovery{}, time.Second); err != ErrInvalid {
		t.Fatal("无事务应拒绝")
	}
	dsn := os.Getenv("APP_COMMAND_TEST_DSN")
	if dsn == "" {
		t.Skip("需专用 PostgreSQL：python3 scripts/check-migrations.py")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal("数据库连接失败")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	log := zap.NewNop()
	id := "90000000-0000-0000-0000-000000000001"
	if err = migration.Run(ctx, db, log, "up"); err != nil {
		t.Fatal(err)
	}
	if err = migration.ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'合成后台工作主体','verified')", id).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DELETE FROM public.accounts WHERE id=?", id)
	if _, err = LockPlan(ctx, db, Discovery{}, time.Second); err != ErrInvalid {
		t.Fatal("根连接不能冒充已有事务")
	}
	err = db.Connection(func(conn *gorm.DB) error {
		if failure := conn.Exec("SET ROLE liyuanci_domain_worker").Error; failure != nil {
			return failure
		}
		defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
		failure := conn.Transaction(func(db *gorm.DB) error {
			tx, failure := LockPlan(ctx, db, Discovery{Accounts: []string{id}}, time.Second)
			if failure != nil {
				return failure
			}
			guard, present := tx.Guard(id)
			if !present || guard.Verification != "verified" || tx.Now().IsZero() || tx.Actor().ID != "" {
				t.Error("后台锁不伪造请求主体")
			}
			return nil
		})
		if failure != nil {
			return failure
		}
		if conn.Exec("UPDATE public.accounts SET verification='verified' WHERE id=?", id).Error == nil {
			t.Error("取得锁不应赋予账号写权限")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Connection(func(conn *gorm.DB) error {
		if failure := conn.Exec("SET ROLE liyuanci_worker").Error; failure != nil {
			return failure
		}
		defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
		if conn.Exec("SELECT * FROM public.lock_command_account(?)", id).Error == nil {
			t.Error("探针不应调用业务 guard")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
