package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

type sendFunc func(context.Context, Event) error

func (f sendFunc) Send(ctx context.Context, event Event) error { return f(ctx, event) }
func route() Route {
	return Route{Kind: "synthetic.scan", TargetKind: "wish", EventType: "content.scan"}
}
func configuration() Config {
	return Config{
		DatabaseTimeout: time.Second,
		SendTimeout:     100 * time.Millisecond,
		LeaseDuration:   time.Second,
		RetryDelay:      time.Millisecond,
		MaxAttempts:     2,
	}
}
func draft(n int) Draft {
	return Draft{WorkID: uid(n), OperationID: uid(n + 100), EventID: uid(n + 200), AggregateID: uid(n + 300), AggregateVersion: 1}
}
func uid(n int) string { return fmt.Sprintf("70000000-0000-0000-0000-%012d", n) }

// T007/REQ-006、007、015、017、018：禁止任意载荷/动态身份类型，缺失依赖失败关闭。
func TestContracts(t *testing.T) {
	if _, err := NewRelay(nil, zap.NewNop(), sendFunc(func(context.Context, Event) error { return nil }), route(), configuration()); err != ErrInvalid {
		t.Fatal("无数据库应拒绝")
	}
	if validRoute(Route{Kind: "body contains secret", TargetKind: "wish", EventType: "scan"}) {
		t.Fatal("任意正文不能成为路由")
	}
	encoded, err := json.Marshal(Event{EventID: uid(1), EventType: "content.scan", AggregateID: uid(2), AggregateVersion: 1, CorrelationID: uid(3), CreatedAt: time.Unix(1, 0).UTC()})
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

// T007/REQ-006、007、015、017、018、020：真 PostgreSQL 事务/租约，Sender 为合成故障适配器。
func TestPostgresDurable(t *testing.T) {
	dsn := os.Getenv("APP_DURABLE_TEST_DSN")
	if dsn == "" {
		t.Skip("需专用数据库：python3 scripts/check-migrations.py")
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
	if err = migration.ApplyPermissions(ctx, db, log); err != nil {
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
	appendWork := func(n int) {
		t.Helper()
		if failure := db.Transaction(func(tx *gorm.DB) error { return Append(ctx, tx, log, route(), draft(n)) }); failure != nil {
			t.Fatal(failure)
		}
	}
	clear := func() { exec("DELETE FROM public.outbox_events"); exec("DELETE FROM public.work_items") }
	relay := func(sender Sender) *Relay {
		t.Helper()
		got, failure := NewRelay(db, log, sender, route(), configuration())
		if failure != nil {
			t.Fatal(failure)
		}
		return got
	}
	state := func(id string) string {
		var status string
		if failure := db.Raw("SELECT status FROM public.work_items WHERE id=?", id).Scan(&status).Error; failure != nil {
			t.Fatal(failure)
		}
		return status
	}
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
	t.Run("并发领取与提交后发送", func(t *testing.T) {
		clear()
		appendWork(2)
		var calls atomic.Int32
		r := relay(sendFunc(func(_ context.Context, event Event) error {
			calls.Add(1)
			if event.CorrelationID != draft(2).WorkID {
				t.Error("关联工作引用错误")
			}
			// 外部连接此时能拿到工作锁，证明没有持有领取事务跨越外部发送。
			return db.Transaction(func(tx *gorm.DB) error {
				return tx.Exec("SELECT id FROM public.work_items WHERE id=? FOR UPDATE NOWAIT", event.CorrelationID).Error
			})
		}))
		var wg sync.WaitGroup
		failures := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, failure := r.Once(ctx); failures <- failure }()
		}
		wg.Wait()
		close(failures)
		for failure := range failures {
			if failure != nil {
				t.Fatal(failure)
			}
		}
		if calls.Load() != 1 {
			t.Fatal("有效租约期间只能发送一次")
		}
		var sent bool
		db.Raw("SELECT sent_at IS NOT NULL FROM public.outbox_events").Scan(&sent)
		if !sent {
			t.Fatal("成功确认未落库")
		}
	})
	t.Run("临时重试与异常待办", func(t *testing.T) {
		clear()
		appendWork(3)
		r := relay(sendFunc(func(context.Context, Event) error { return fmt.Errorf("SYNTHETIC_SECRET: %w", ErrTemporary) }))
		if _, failure := r.Once(ctx); failure != ErrTemporary {
			t.Fatal(failure)
		}
		if state(draft(3).WorkID) != "pending" {
			t.Fatal("首次临时失败不终结")
		}
		exec("UPDATE public.outbox_events SET next_at=clock_timestamp()-interval '1 second'")
		if _, failure := r.Once(ctx); failure != ErrConfiguration {
			t.Fatal(failure)
		}
		if state(draft(3).WorkID) != "attention" {
			t.Fatal("重试门槛应进入异常待办")
		}
		if claimed, failure := r.Once(ctx); failure != nil || claimed {
			t.Fatal("异常待办不能自动重放")
		}
	})
	t.Run("配置故障与发送恢复", func(t *testing.T) {
		for _, panicNow := range []bool{false, true} {
			clear()
			appendWork(4)
			r := relay(sendFunc(func(context.Context, Event) error {
				if panicNow {
					panic("SYNTHETIC_SECRET")
				}
				return errors.New("SYNTHETIC_SECRET")
			}))
			if _, failure := r.Once(ctx); failure != ErrConfiguration {
				t.Fatal(failure)
			}
			if state(draft(4).WorkID) != "attention" {
				t.Fatal("未知发送故障需待办")
			}
		}
	})
	t.Run("失效租约与稳定重复事件", func(t *testing.T) {
		clear()
		appendWork(5)
		var first Event
		old := relay(sendFunc(func(_ context.Context, event Event) error {
			first = event
			exec("UPDATE public.outbox_events SET lease_until=clock_timestamp()-interval '1 second'")
			return nil
		}))
		if _, failure := old.Once(ctx); failure != ErrLeaseLost {
			t.Fatal("过期持有者不得确认", failure)
		}
		current := relay(sendFunc(func(_ context.Context, event Event) error {
			if event != first {
				t.Error("重投必须保持稳定事件和技术时间")
			}
			return nil
		}))
		if _, failure := current.Once(ctx); failure != nil {
			t.Fatal(failure)
		}
	})
	t.Run("旧转发者不得覆盖新租约", func(t *testing.T) {
		clear()
		appendWork(6)
		newer := relay(sendFunc(func(context.Context, Event) error { return nil }))
		old := relay(sendFunc(func(context.Context, Event) error {
			exec("UPDATE public.outbox_events SET lease_until=clock_timestamp()-interval '1 second'")
			if _, failure := newer.Once(ctx); failure != nil {
				t.Error(failure)
			}
			return ErrTemporary
		}))
		if _, failure := old.Once(ctx); failure != ErrLeaseLost {
			t.Fatal(failure)
		}
		if state(draft(6).WorkID) != "pending" {
			t.Fatal("旧持有者不得写失败或待办")
		}
	})
	t.Run("取消与完成不复活", func(t *testing.T) {
		for _, status := range []string{"cancelled", "completed"} {
			clear()
			appendWork(7)
			exec("UPDATE public.work_items SET status=?", status)
			r := relay(sendFunc(func(context.Context, Event) error { t.Error("终态不应再次转发"); return nil }))
			if claimed, failure := r.Once(ctx); failure != nil || claimed {
				t.Fatal("终态应跳过")
			}
		}
		clear()
		appendWork(8)
		r := relay(sendFunc(func(context.Context, Event) error {
			exec("UPDATE public.work_items SET status='cancelled'")
			return ErrConfiguration
		}))
		if _, failure := r.Once(ctx); failure != ErrConfiguration {
			t.Fatal(failure)
		}
		if state(draft(8).WorkID) != "cancelled" {
			t.Fatal("发送中取消不得被待办覆盖")
		}
	})
	t.Run("确认前中断耗尽预算", func(t *testing.T) {
		clear()
		appendWork(10)
		exec("UPDATE public.outbox_events SET attempts=2,lease_until=clock_timestamp()-interval '1 second'")
		r := relay(sendFunc(func(context.Context, Event) error { t.Error("预算已耗尽不得发送"); return nil }))
		if claimed, failure := r.Once(ctx); failure != nil || claimed {
			t.Fatal("预算耗尽仅标记待办", failure)
		}
		if state(draft(10).WorkID) != "attention" {
			t.Fatal("确认前中断也应进入待办")
		}
	})
	t.Run("领域工作组实际权限", func(t *testing.T) {
		clear()
		appendWork(9)
		failure := db.Connection(func(conn *gorm.DB) error {
			if failure := conn.Exec("SET ROLE liyuanci_domain_worker").Error; failure != nil {
				return failure
			}
			defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
			r, failure := NewRelay(conn, log, sendFunc(func(context.Context, Event) error { return nil }), route(), configuration())
			if failure != nil {
				return failure
			}
			_, failure = r.Once(ctx)
			return failure
		})
		if failure != nil {
			t.Fatal(failure)
		}
	})
	entries := observed.FilterMessage("发件箱转发完成").All()
	if len(entries) == 0 {
		t.Fatal("缺少转发完成日志")
	}
	for _, entry := range entries {
		fields := entry.ContextMap()
		if fields["duration"] == nil || fields["result"] == nil || fields["claimed"] == nil {
			t.Fatal("缺少完成字段")
		}
		if strings.Contains(fmt.Sprint(fields), "SYNTHETIC_SECRET") {
			t.Fatal("供应商错误泄漏")
		}
	}
	for _, entry := range observed.FilterMessage("持久工作暂存完成").All() {
		if entry.ContextMap()["result"] == "success" {
			t.Fatal("不能把事务暂存记成持久提交")
		}
	}
}
