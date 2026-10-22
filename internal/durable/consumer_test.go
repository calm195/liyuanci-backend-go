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

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

// T007/REQ-006、007、015、017、018、020：真实数据库消费边界，事件仅为技术唤醒，无 Broker 替身验收。
func TestPostgresConsumer(t *testing.T) {
	dsn := os.Getenv("APP_DURABLE_TEST_DSN")
	if dsn == "" {
		t.Skip("需专用 PostgreSQL：按模块要求提供隔离依赖后运行相关集成测试")
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
	owner := uid(888)
	exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'合成消费账号','verified')", owner)
	defer exec("DELETE FROM public.accounts WHERE id=?", owner)
	clear := func() {
		exec("DELETE FROM public.consumer_receipts")
		exec("DELETE FROM public.outbox_events")
		exec("DELETE FROM public.work_items")
		exec("UPDATE public.accounts SET version=1,verification='verified' WHERE id=?", owner)
	}
	defer clear()
	makeEvent := func(n int) Event {
		t.Helper()
		d := draft(n)
		d.AggregateID = owner
		if failure := db.Transaction(func(tx *gorm.DB) error { return Append(ctx, tx, log, route(), d) }); failure != nil {
			t.Fatal(failure)
		}
		var event Event
		if failure := db.Raw("SELECT event_id,event_type,aggregate_id,aggregate_version,work_id AS correlation_id,created_at FROM public.outbox_events WHERE event_id=?", d.EventID).Scan(&event).Error; failure != nil {
			t.Fatal(failure)
		}
		return event
	}
	config := ConsumerConfig{Timeout: 3 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 2}
	var calls atomic.Int32
	definition := ConsumerDefinition{
		ConsumerKind: "synthetic.consumer", Route: route(),
		Discover: func(ctx context.Context, reader *gorm.DB, work Work) (command.Discovery, error) {
			var ids []string
			if failure := reader.WithContext(ctx).Raw("SELECT id FROM public.accounts WHERE id=?", work.TargetID).Scan(&ids).Error; failure != nil {
				return command.Discovery{}, failure
			}
			return command.Discovery{Accounts: ids}, nil
		},
		Apply: func(ctx context.Context, tx *command.Tx, work Work) (Decision, error) {
			calls.Add(1)
			guard, present := tx.Guard(work.TargetID)
			if !present || guard.Verification != "verified" || guard.Lifecycle != "active" || guard.Version != work.TargetVersion {
				return Obsolete, nil
			}
			return Complete, tx.DB().WithContext(ctx).Exec("UPDATE public.accounts SET version=version+1 WHERE id=?", work.TargetID).Error
		},
	}
	consumer := func(d ConsumerDefinition) *Consumer {
		t.Helper()
		got, failure := NewConsumer(db, log, d, config)
		if failure != nil {
			t.Fatal(failure)
		}
		return got
	}
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var n int64
		if failure := db.Raw(query, args...).Scan(&n).Error; failure != nil {
			t.Fatal(failure)
		}
		return n
	}
	status := func(id string) string {
		var value string
		if failure := db.Raw("SELECT status FROM public.work_items WHERE id=?", id).Scan(&value).Error; failure != nil {
			t.Fatal(failure)
		}
		return value
	}
	t.Run("同工作并发只提交一次", func(t *testing.T) {
		clear()
		event := makeEvent(50)
		c := consumer(definition)
		var wg sync.WaitGroup
		failures := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, failure := c.Consume(ctx, event); failures <- failure }()
		}
		wg.Wait()
		close(failures)
		for failure := range failures {
			if failure != nil {
				t.Fatal(failure)
			}
		}
		if calls.Load() != 1 || scalar("SELECT version FROM public.accounts WHERE id=?", owner) != 2 || scalar("SELECT count(*) FROM public.consumer_receipts") != 1 || status(event.CorrelationID) != "completed" {
			t.Fatal("业务结果、终态及回执应只提交一次")
		}
		out, failure := c.Consume(ctx, event)
		if failure != nil || !out.Replayed || out.Applied {
			t.Fatal("重复消费应无操作")
		}
	})
	t.Run("拒绝伪造与缺失事件", func(t *testing.T) {
		clear()
		event := makeEvent(51)
		c := consumer(definition)
		event.AggregateVersion++
		out, failure := c.Consume(ctx, event)
		if failure != nil || !out.Missing {
			t.Fatal("事件不能改变持久版本")
		}
		if scalar("SELECT count(*) FROM public.consumer_receipts") != 0 {
			t.Fatal("缺失不能产生回执")
		}
		event.EventID = uid(99999)
		out, failure = c.Consume(ctx, event)
		if failure != nil || !out.Missing {
			t.Fatal("未知事件不能重建工作")
		}
	})
	t.Run("取消晚到不依赖已删目标", func(t *testing.T) {
		clear()
		event := makeEvent(52)
		exec("UPDATE public.work_items SET status='cancelled'")
		d := definition
		d.Discover = func(context.Context, *gorm.DB, Work) (command.Discovery, error) {
			t.Error("终态不应发现业务对象")
			return command.Discovery{}, nil
		}
		d.Apply = func(context.Context, *command.Tx, Work) (Decision, error) {
			t.Error("取消不应执行业务")
			return Complete, nil
		}
		out, failure := consumer(d).Consume(ctx, event)
		if failure != nil || !out.Cancelled || scalar("SELECT count(*) FROM public.consumer_receipts") != 1 || status(event.CorrelationID) != "cancelled" {
			t.Fatal("取消应幂等终结")
		}
	})
	t.Run("当前资格变更取消旧工作", func(t *testing.T) {
		clear()
		event := makeEvent(53)
		exec("UPDATE public.accounts SET verification='rejected' WHERE id=?", owner)
		out, failure := consumer(definition).Consume(ctx, event)
		if failure != nil || !out.Cancelled || scalar("SELECT version FROM public.accounts WHERE id=?", owner) != 1 || status(event.CorrelationID) != "cancelled" {
			t.Fatal("不能信任事件中的旧资格")
		}
	})
	t.Run("目标版本与工作版本独立", func(t *testing.T) {
		clear()
		event := makeEvent(59)
		exec("UPDATE public.accounts SET version=2 WHERE id=?", owner)
		exec("UPDATE public.work_items SET version=7")
		out, failure := consumer(definition).Consume(ctx, event)
		if failure != nil || !out.Cancelled || scalar("SELECT version FROM public.accounts WHERE id=?", owner) != 2 {
			t.Fatal("不得用工作版本替代目标内容版本")
		}
	})
	t.Run("失败回滚与持久重试预算", func(t *testing.T) {
		clear()
		event := makeEvent(54)
		d := definition
		d.Apply = func(ctx context.Context, tx *command.Tx, work Work) (Decision, error) {
			if failure := tx.DB().WithContext(ctx).Exec("UPDATE public.accounts SET version=99 WHERE id=?", work.TargetID).Error; failure != nil {
				return "", failure
			}
			return "", ErrTemporary
		}
		c := consumer(d)
		for attempt := 1; attempt <= 2; attempt++ {
			_, failure := c.Consume(ctx, event)
			if failure != ErrTemporary {
				t.Fatal(failure)
			}
			if scalar("SELECT version FROM public.accounts WHERE id=?", owner) != 1 || scalar("SELECT count(*) FROM public.consumer_receipts") != 0 {
				t.Fatal("失败不得提交业务或回执")
			}
			if attempt == 1 {
				if status(event.CorrelationID) != "retry_wait" {
					t.Fatal("应持久等待重试")
				}
				exec("UPDATE public.work_items SET next_at=clock_timestamp()-interval '1 second'")
			}
		}
		if status(event.CorrelationID) != "attention" {
			t.Fatal("达到门槛应进入异常待办")
		}
		out, failure := c.Consume(ctx, event)
		if failure != nil || !out.Deferred {
			t.Fatal("异常待办不得自动重放")
		}
	})
	t.Run("panic回滚后才记录待办", func(t *testing.T) {
		clear()
		event := makeEvent(55)
		d := definition
		d.Apply = func(ctx context.Context, tx *command.Tx, work Work) (Decision, error) {
			tx.DB().WithContext(ctx).Exec("UPDATE public.accounts SET version=99 WHERE id=?", work.TargetID)
			panic("SYNTHETIC_CONSUMER_SECRET")
		}
		if _, failure := consumer(d).Consume(ctx, event); failure != ErrConfiguration {
			t.Fatal(failure)
		}
		if scalar("SELECT version FROM public.accounts WHERE id=?", owner) != 1 || scalar("SELECT count(*) FROM public.consumer_receipts") != 0 || status(event.CorrelationID) != "attention" {
			t.Fatal("恢复不得提交部分业务")
		}
	})
	t.Run("调度失败不得覆盖并发取消", func(t *testing.T) {
		clear()
		event := makeEvent(56)
		d := definition
		d.Apply = func(context.Context, *command.Tx, Work) (Decision, error) { return "", ErrTemporary }
		// 在初次发现后先取消，锁后应走终态，不进入 Apply 或重试调度。
		var once sync.Once
		discover := d.Discover
		d.Discover = func(ctx context.Context, reader *gorm.DB, work Work) (command.Discovery, error) {
			plan, failure := discover(ctx, reader, work)
			once.Do(func() { exec("UPDATE public.work_items SET status='cancelled',version=version+1") })
			return plan, failure
		}
		out, failure := consumer(d).Consume(ctx, event)
		if failure != nil || !out.Cancelled || status(event.CorrelationID) != "cancelled" {
			t.Fatal("取消不能被重试复活")
		}
	})
	t.Run("未到执行时间不提交回执", func(t *testing.T) {
		clear()
		event := makeEvent(57)
		exec("UPDATE public.work_items SET next_at=clock_timestamp()+interval '1 hour'")
		out, failure := consumer(definition).Consume(ctx, event)
		if failure != nil || !out.Deferred || scalar("SELECT count(*) FROM public.consumer_receipts") != 0 {
			t.Fatal("应等待数据库调度")
		}
	})
	t.Run("实际领域组权限与丢失确认重放", func(t *testing.T) {
		clear()
		event := makeEvent(58)
		failure := db.Connection(func(conn *gorm.DB) error {
			if failure := conn.Exec("SET ROLE liyuanci_domain_worker").Error; failure != nil {
				return failure
			}
			defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
			// 当前权限组不写账号；合成领域回调只读当前 guard 并完成技术工作。
			d := definition
			d.Apply = func(context.Context, *command.Tx, Work) (Decision, error) { return Complete, nil }
			c, failure := NewConsumer(conn, log, d, config)
			if failure != nil {
				return failure
			}
			_, failure = c.Consume(ctx, event)
			return failure
		})
		if failure != nil {
			t.Fatal(failure)
		}
		encoded, _ := json.Marshal(event)
		var resent Event
		if failure = json.Unmarshal(encoded, &resent); failure != nil {
			t.Fatal(failure)
		}
		out, failure := consumer(definition).Consume(ctx, resent)
		if failure != nil || !out.Replayed {
			t.Fatal("提交后运输确认丢失应去重")
		}
	})
	// T007/REQ-018、020：GORM 更新与回执写入仍覆盖成功、失败两个完成边界。
	for _, result := range []string{"success", "failure"} {
		if observed.FilterMessage("持久工作消费完成").FilterField(zap.String("result", result)).Len() == 0 {
			t.Fatalf("缺少消费完成结果：%s", result)
		}
	}
	for _, entry := range observed.FilterMessage("持久工作消费完成").All() {
		fields := entry.ContextMap()
		if fields["duration"] == nil || fields["result"] == nil || fields["consumer"] == nil {
			t.Fatal("缺少完成日志")
		}
		if strings.Contains(fmt.Sprint(fields), "SYNTHETIC_CONSUMER_SECRET") {
			t.Fatal("panic原文泄漏")
		}
	}
	if _, failure := NewConsumer(nil, log, definition, config); !errors.Is(failure, ErrInvalid) {
		t.Fatal("缺失数据库应拒绝")
	}
}
