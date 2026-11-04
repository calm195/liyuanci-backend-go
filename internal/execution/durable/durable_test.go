package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

func route() Route {
	return Route{Kind: "synthetic.scan", TargetKind: "wish", EventType: "content.scan"}
}

func draft(n int) Draft {
	return Draft{
		WorkID:           uid(n),
		OperationID:      uid(n + 100),
		EventID:          uid(n + 200),
		AggregateID:      uid(n + 300),
		AggregateVersion: 1,
	}
}

func uid(n int) string { return fmt.Sprintf("70000000-0000-0000-0000-%012d", n) }

// T007/REQ-006、007、015、017、018：禁止任意载荷/动态身份类型，缺失依赖失败关闭。
func TestContracts(t *testing.T) {
	if validRoute(Route{Kind: "body contains secret", TargetKind: "wish", EventType: "scan"}) {
		t.Fatal("任意正文不能成为路由")
	}
	encoded, err := json.Marshal(Event{
		EventID:          uid(1),
		EventType:        "content.scan",
		AggregateID:      uid(2),
		AggregateVersion: 1,
		CorrelationID:    uid(3),
		CreatedAt:        time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(encoded, &fields); err != nil || len(fields) != 6 {
		t.Fatal("唤醒仅允许六个技术字段")
	}
	if err = Append(context.Background(), nil, zap.NewNop(), route(), draft(1)); err != ErrInvalid {
		t.Fatal("缺少事务")
	}
}

// T007/REQ-006、007、015、017、018、020：真实 PostgreSQL 事务验证业务工作与事件关联同提交、回滚及唯一性。
func TestPostgresDurable(t *testing.T) {
	dsn := testconfig.Value("durable_test_dsn")
	if dsn == "" {
		t.Skip("需专用数据库：按模块要求提供隔离依赖后运行相关集成测试")
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
	core, observed := observer.New(zap.InfoLevel)
	log := zap.New(core)
	if err = migration.Run(ctx, db, log, "up"); err != nil {
		t.Fatal(err)
	}
	if err = testconfig.ApplyDatabaseRoles(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if failure := db.Exec(query, args...).Error; failure != nil {
			t.Fatal(failure)
		}
	}
	count := func(table string) int64 {
		t.Helper()
		var n int64
		if failure := db.Raw("SELECT count(*) FROM public." + table).Scan(&n).Error; failure != nil {
			t.Fatal(failure)
		}
		return n
	}
	// 专用测试库可重复运行，事务夹具账号随本测试退出清理。
	defer exec("DELETE FROM public.accounts WHERE id=?", uid(999))
	t.Run("业务工作事件同事务", func(t *testing.T) {
		exec("INSERT INTO public.accounts(id,nickname) VALUES (?,'合成事务账号')", uid(999))
		if failure := Append(ctx, db, log, route(), draft(1)); failure != ErrInvalid {
			t.Fatal("根连接不能暂存")
		}
		for _, rollback := range []bool{true, false} {
			failure := db.Transaction(func(tx *gorm.DB) error {
				if failure := tx.Exec("UPDATE public.accounts SET version=version+1 WHERE id=?", uid(999)).Error; failure != nil {
					return failure
				}
				if failure := Append(ctx, tx, log, route(), draft(1)); failure != nil {
					return failure
				}
				if rollback {
					return errors.New("SYNTHETIC_ROLLBACK")
				}
				return nil
			})
			if rollback && failure == nil {
				t.Fatal("应回滚")
			}
			if !rollback && failure != nil {
				t.Fatal(failure)
			}
			want := int64(1)
			if rollback {
				want = 0
			}
			if count("work_items") != want || count("outbox_events") != want {
				t.Fatal("工作和事件必须原子写入")
			}
			var version int64
			db.Raw("SELECT version FROM public.accounts WHERE id=?", uid(999)).Scan(&version)
			if version != want+1 {
				t.Fatal("业务版本必须同步回滚")
			}
		}
		// 事件冲突不能留下半个工作；操作标识重复也不产生第二条工作。
		another := draft(2)
		another.EventID = draft(1).EventID
		if failure := db.Transaction(func(tx *gorm.DB) error { return Append(ctx, tx, log, route(), another) }); failure != ErrUnavailable {
			t.Fatal("事件冲突应拒绝")
		}
		if count("work_items") != 1 {
			t.Fatal("冲突产生孤立工作")
		}
	})
	for _, entry := range observed.FilterMessage("持久工作暂存完成").All() {
		if entry.ContextMap()["result"] == "success" {
			t.Fatal("不能把事务暂存记成持久提交")
		}
	}
}
