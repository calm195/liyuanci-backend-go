package durable

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

// T007/REQ-006、007、015、017、018、020：补偿直接走当前持久源，重驱动经真实命令会话与数据库权限验证。
func TestPostgresRecovery(t *testing.T) {
	dsn := os.Getenv("APP_DURABLE_TEST_DSN")
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
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var n int64
		if failure := db.Raw(query, args...).Scan(&n).Error; failure != nil {
			t.Fatal(failure)
		}
		return n
	}
	owner, admin := uid(9901), uid(9902)
	exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'合成补偿目标','verified')", owner)
	exec("INSERT INTO public.admin_accounts(id,credential_ref) VALUES (?,'synthetic-redrive-provider')", admin)
	exec("INSERT INTO public.admin_role_bindings(admin_id,role) VALUES (?,'super_admin')", admin)
	hash := sha256.Sum256([]byte("SYNTHETIC_REDRIVE_TOKEN"))
	exec("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uid(9903), admin, hash[:])
	defer exec("DELETE FROM public.admin_accounts WHERE id=?", admin)
	defer exec("DELETE FROM public.accounts WHERE id=?", owner)
	clear := func() {
		exec("DELETE FROM public.command_receipts WHERE actor_id=?", admin)
		exec("DELETE FROM public.consumer_receipts")
		exec("DELETE FROM public.outbox_events")
		exec("DELETE FROM public.work_items")
		exec("UPDATE public.accounts SET verification='verified',version=1 WHERE id=?", owner)
	}
	defer clear()
	makeWork := func(n int, status string) Event {
		t.Helper()
		d := draft(n)
		d.AggregateID = owner
		if failure := db.Transaction(func(tx *gorm.DB) error { return Append(ctx, tx, log, route(), d) }); failure != nil {
			t.Fatal(failure)
		}
		if status != "pending" {
			exec("UPDATE public.work_items SET status=?,version=5,attempts=3,lease_until=clock_timestamp()-interval '1 second' WHERE id=?", status, d.WorkID)
		}
		var event Event
		if failure := db.Raw("SELECT event_id,event_type,aggregate_id,aggregate_version,work_id AS correlation_id,created_at FROM public.outbox_events WHERE event_id=?", d.EventID).Scan(&event).Error; failure != nil {
			t.Fatal(failure)
		}
		return event
	}
	var calls atomic.Int32
	definition := ConsumerDefinition{ConsumerKind: "synthetic.recovery", Route: route(),
		Discover: func(ctx context.Context, reader *gorm.DB, work Work) (command.Discovery, error) {
			var ids []string
			if failure := reader.WithContext(ctx).Raw("SELECT id FROM public.accounts WHERE id=?", work.TargetID).Scan(&ids).Error; failure != nil {
				return command.Discovery{}, failure
			}
			return command.Discovery{Accounts: ids}, nil
		},
		Apply: func(ctx context.Context, tx *command.Tx, work Work) (Decision, error) {
			calls.Add(1)
			guard, ok := tx.Guard(work.TargetID)
			if !ok || guard.Verification != "verified" {
				return Obsolete, nil
			}
			return Complete, tx.DB().WithContext(ctx).Exec("UPDATE public.accounts SET version=version+1 WHERE id=?", owner).Error
		},
	}
	c, err := NewConsumer(db, log, definition, ConsumerConfig{Timeout: 3 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	policy := RedrivePolicy{AllowedRoles: []string{"super_admin"}, Recheck: func(_ context.Context, tx *command.Tx, work Work) (RedriveDecision, error) {
		guard, ok := tx.Guard(work.TargetID)
		if !ok || guard.Verification != "verified" || guard.Version != work.TargetVersion {
			return Invalidate, nil
		}
		return Resume, nil
	}}
	commandConfig := command.Config{Timeout: 3 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 1}
	commandDefinition := func(input RedriveRequest, p RedrivePolicy, afterError bool) command.Definition {
		d, failure := c.RedriveCommand(p, input)
		if failure != nil {
			t.Fatal(failure)
		}
		if afterError {
			apply := d.Apply
			d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
				_, failure := apply(ctx, tx)
				if failure != nil {
					return "", failure
				}
				return "", command.ErrConflict
			}
		}
		return d
	}

	engine, err := command.New(db, log, commandConfig)
	if err != nil {
		t.Fatal(err)
	}
	run := func(n int, key string, p RedrivePolicy, expected int64, afterError bool) (command.Outcome, error) {
		input := RedriveRequest{WorkID: draft(n).WorkID, ExpectedVersion: expected}
		return engine.Execute(ctx, commandDefinition(input, p, afterError), command.Input{Token: "SYNTHETIC_REDRIVE_TOKEN", IdempotencyKey: key, RequestID: "redrive-1", Request: input})
	}
	status := func(id string) string {
		var s string
		if failure := db.Raw("SELECT status FROM public.work_items WHERE id=?", id).Scan(&s).Error; failure != nil {
			t.Fatal(failure)
		}
		return s
	}
	t.Run("已确认丢唤醒由数据库补偿", func(t *testing.T) {
		clear()
		calls.Store(0)
		event := makeWork(1000, "pending")
		exec("UPDATE public.outbox_events SET sent_at=clock_timestamp()")
		out, failure := c.Compensate(ctx, 10)
		if failure != nil || out.Applied != 1 || status(event.CorrelationID) != "completed" {
			t.Fatal("应补偿已发送但未消费工作", out, failure)
		}
		if _, failure = c.Compensate(ctx, 10); failure != nil || calls.Load() != 1 {
			t.Fatal("补偿不能重复产生效果")
		}
		got, failure := c.Consume(ctx, event)
		if failure != nil || !got.Replayed {
			t.Fatal("晚到 MQ 应去重")
		}
	})
	t.Run("到期与终态及上限", func(t *testing.T) {
		clear()
		makeWork(1001, "pending")
		makeWork(1002, "retry_wait")
		makeWork(1003, "leased")
		makeWork(1004, "cancelled")
		makeWork(1005, "completed")
		makeWork(1006, "attention")
		makeWork(1007, "leased")
		makeWork(1008, "pending")
		exec("UPDATE public.work_items SET lease_until=clock_timestamp()+interval '1 hour' WHERE id=?", draft(1007).WorkID)
		exec("UPDATE public.work_items SET next_at=clock_timestamp()+interval '1 hour' WHERE id=?", draft(1008).WorkID)
		out, failure := c.Compensate(ctx, 2)
		if failure != nil || out.Scanned != 2 {
			t.Fatal(out, failure)
		}
		out, failure = c.Compensate(ctx, 10)
		if failure != nil || out.Scanned != 1 {
			t.Fatal(out, failure)
		}
		if status(draft(1004).WorkID) != "cancelled" || status(draft(1006).WorkID) != "attention" || status(draft(1007).WorkID) != "leased" {
			t.Fatal("终态或有效租约不能补偿复活")
		}
		if _, failure = c.Compensate(ctx, 101); failure != ErrInvalid {
			t.Fatal("必须有界")
		}
	})
	t.Run("缺失版本关联不猜测重建", func(t *testing.T) {
		clear()
		event := makeWork(1009, "pending")
		exec("DELETE FROM public.outbox_events")
		out, failure := c.Compensate(ctx, 10)
		if failure != nil || out.Attention != 1 || status(event.CorrelationID) != "attention" || scalar("SELECT count(*) FROM public.outbox_events") != 0 {
			t.Fatal("缺失关联应待办且不造事件", out, failure)
		}
		if _, failure = run(1009, "no-source", RedrivePolicy{AllowedRoles: policy.AllowedRoles, Recheck: func(context.Context, *command.Tx, Work) (RedriveDecision, error) { return Resume, nil }}, 2, false); failure != command.ErrConflict {
			t.Fatal("缺目标版本不能恢复", failure)
		}
	})
	t.Run("并发补偿不重复提交", func(t *testing.T) {
		clear()
		makeWork(1010, "pending")
		calls.Store(0)
		var wg sync.WaitGroup
		failures := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, failure := c.Compensate(ctx, 10); failures <- failure }()
		}
		wg.Wait()
		close(failures)
		for failure := range failures {
			if failure != nil {
				t.Fatal(failure)
			}
		}
		if calls.Load() != 1 {
			t.Fatal("并行扫描应由消费去重")
		}
	})
	t.Run("重驱动保持操作并替代旧事件", func(t *testing.T) {
		clear()
		event := makeWork(1011, "attention")
		operation := draft(1011).OperationID
		out, failure := run(1011, "resume", policy, 5, false)
		if failure != nil || out.Replayed || status(event.CorrelationID) != "pending" {
			t.Fatal(out, failure)
		}
		out, failure = run(1011, "resume", policy, 5, false)
		if failure != nil || !out.Replayed {
			t.Fatal("命令重放不得再次复位预算", failure)
		}
		if scalar("SELECT count(*) FROM public.work_items WHERE operation_id=?", operation) != 1 || scalar("SELECT count(*) FROM public.outbox_events") != 2 || scalar("SELECT count(*) FROM public.outbox_events WHERE superseded_at IS NULL") != 1 {
			t.Fatal("只能替代唤醒，不能重建工作或操作")
		}
		got, failure := c.Consume(ctx, event)
		if failure != nil || !got.Missing {
			t.Fatal("替代后的旧事件应失效")
		}
		if _, failure = c.Compensate(ctx, 10); failure != nil || status(event.CorrelationID) != "completed" {
			t.Fatal("新唤醒可补偿完成", failure)
		}
	})
	t.Run("并发重驱动只创建一条新唤醒", func(t *testing.T) {
		clear()
		makeWork(1021, "attention")
		var wg sync.WaitGroup
		failures := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, failure := run(1021, "parallel-resume", policy, 5, false)
				failures <- failure
			}()
		}
		wg.Wait()
		close(failures)
		for failure := range failures {
			if failure != nil {
				t.Fatal(failure)
			}
		}
		if scalar("SELECT count(*) FROM public.work_items") != 1 || scalar("SELECT count(*) FROM public.outbox_events") != 2 || scalar("SELECT count(*) FROM public.outbox_events WHERE superseded_at IS NULL") != 1 {
			t.Fatal("并发命令必须复用回执且只创建新唤醒一次")
		}
	})
	t.Run("重放仍复核当前授权", func(t *testing.T) {
		clear()
		makeWork(1018, "attention")
		if _, failure := run(1018, "replay-auth", policy, 5, false); failure != nil {
			t.Fatal(failure)
		}
		denied := policy
		denied.Recheck = func(context.Context, *command.Tx, Work) (RedriveDecision, error) { return "", command.ErrForbidden }
		if _, failure := run(1018, "replay-auth", denied, 5, false); failure != command.ErrForbidden {
			t.Fatal("回执不能绕过当前对象授权", failure)
		}
		exec("DELETE FROM public.admin_role_bindings WHERE admin_id=?", admin)
		if _, failure := run(1018, "replay-auth", policy, 5, false); failure != command.ErrForbidden {
			t.Fatal("回执不能绕过当前角色", failure)
		}
		exec("INSERT INTO public.admin_role_bindings(admin_id,role) VALUES (?,'super_admin')", admin)
	})
	t.Run("旧版本与终态拒绝重驱动", func(t *testing.T) {
		clear()
		event := makeWork(1012, "attention")
		if _, failure := run(1012, "stale", policy, 4, false); failure != command.ErrVersionConflict {
			t.Fatal(failure)
		}
		for _, s := range []string{"cancelled", "completed", "pending"} {
			exec("UPDATE public.work_items SET status=? WHERE id=?", s, event.CorrelationID)
			if _, failure := run(1012, "terminal-"+s, policy, 5, false); failure != command.ErrConflict {
				t.Fatal(s, failure)
			}
		}
		if scalar("SELECT count(*) FROM public.outbox_events") != 1 {
			t.Fatal("拒绝不能产生事件")
		}
	})
	t.Run("当前资格和回执禁止复活", func(t *testing.T) {
		clear()
		event := makeWork(1013, "attention")
		exec("UPDATE public.accounts SET version=2 WHERE id=?", owner)
		if _, failure := run(1013, "obsolete", policy, 5, false); failure != nil || status(event.CorrelationID) != "cancelled" {
			t.Fatal("目标过时应无操作终结", failure)
		}
		if scalar("SELECT count(*) FROM public.consumer_receipts") != 1 || scalar("SELECT count(*) FROM public.outbox_events WHERE superseded_at IS NULL") != 0 {
			t.Fatal("无操作终结应持久化")
		}
		clear()
		makeWork(1014, "attention")
		exec("INSERT INTO public.consumer_receipts(consumer_kind,operation_id) VALUES (?,?)", definition.ConsumerKind, draft(1014).OperationID)
		if _, failure := run(1014, "has-receipt", policy, 5, false); failure != command.ErrConflict {
			t.Fatal("已消费操作不得重驱动", failure)
		}
	})
	t.Run("角色与拒绝及原子回滚", func(t *testing.T) {
		clear()
		makeWork(1015, "attention")
		denied := policy
		denied.AllowedRoles = []string{"review_operator"}
		if _, failure := run(1015, "role", denied, 5, false); failure != command.ErrForbidden {
			t.Fatal(failure)
		}
		denied = policy
		denied.Recheck = func(context.Context, *command.Tx, Work) (RedriveDecision, error) { return "", command.ErrForbidden }
		if _, failure := run(1015, "recheck", denied, 5, false); failure != command.ErrForbidden {
			t.Fatal(failure)
		}
		if _, failure := run(1015, "rollback", policy, 5, true); failure != command.ErrConflict {
			t.Fatal(failure)
		}
		if status(draft(1015).WorkID) != "attention" || scalar("SELECT count(*) FROM public.outbox_events WHERE superseded_at IS NOT NULL") != 0 || scalar("SELECT count(*) FROM public.outbox_events") != 1 {
			t.Fatal("后续命令失败必须回滚全部重驱动变更")
		}
	})
	t.Run("旧转发者不能覆盖新周期", func(t *testing.T) {
		clear()
		event := makeWork(1016, "pending")
		newer, _ := NewRelay(db, log, sendFunc(func(context.Context, Event) error { return nil }), route(), configuration())
		older, _ := NewRelay(db, log, sendFunc(func(context.Context, Event) error {
			exec("UPDATE public.work_items SET status='attention',version=5 WHERE id=?", event.CorrelationID)
			if _, failure := run(1016, "fence", policy, 5, false); failure != nil {
				return failure
			}
			if _, failure := newer.Once(ctx); failure != nil {
				return failure
			}
			return ErrTemporary
		}), route(), configuration())
		if _, failure := older.Once(ctx); failure != ErrLeaseLost {
			t.Fatal("旧租约应失效", failure)
		}
		if status(event.CorrelationID) != "pending" || scalar("SELECT count(*) FROM public.outbox_events WHERE sent_at IS NOT NULL AND superseded_at IS NULL") != 1 {
			t.Fatal("旧持有者不能覆盖新周期")
		}
	})
	t.Run("实际后台命令与领域补偿权限", func(t *testing.T) {
		clear()
		event := makeWork(1017, "attention")
		failure := db.Connection(func(conn *gorm.DB) error {
			if failure := conn.Exec("SET ROLE liyuanci_admin_api").Error; failure != nil {
				return failure
			}
			defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
			engine, failure := command.New(conn, log, commandConfig)
			if failure != nil {
				return failure
			}
			input := RedriveRequest{WorkID: event.CorrelationID, ExpectedVersion: 5}
			_, failure = engine.Execute(ctx, commandDefinition(input, policy, false), command.Input{Token: "SYNTHETIC_REDRIVE_TOKEN", IdempotencyKey: "actual-role", Request: input})
			return failure
		})
		if failure != nil {
			t.Fatal(failure)
		}
		failure = db.Connection(func(conn *gorm.DB) error {
			if failure := conn.Exec("SET ROLE liyuanci_domain_worker").Error; failure != nil {
				return failure
			}
			defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
			d := definition
			d.Apply = func(context.Context, *command.Tx, Work) (Decision, error) { return Complete, nil }
			worker, failure := NewConsumer(conn, log, d, c.config)
			if failure != nil {
				return failure
			}
			_, failure = worker.Compensate(ctx, 10)
			return failure
		})
		if failure != nil {
			t.Fatal(failure)
		}
	})
	t.Run("实际后台失效终结及回执窄权限", func(t *testing.T) {
		clear()
		event := makeWork(1019, "attention")
		exec("UPDATE public.accounts SET version=2 WHERE id=?", owner)
		failure := db.Connection(func(conn *gorm.DB) error {
			if failure := conn.Exec("SET ROLE liyuanci_admin_api").Error; failure != nil {
				return failure
			}
			defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
			engine, failure := command.New(conn, log, commandConfig)
			if failure != nil {
				return failure
			}
			input := RedriveRequest{WorkID: event.CorrelationID, ExpectedVersion: 5}
			if _, failure = engine.Execute(ctx, commandDefinition(input, policy, false), command.Input{Token: "SYNTHETIC_REDRIVE_TOKEN", IdempotencyKey: "actual-obsolete", Request: input}); failure != nil {
				return failure
			}
			if conn.Exec("SELECT completed_at FROM public.consumer_receipts").Error == nil {
				t.Error("后台不得读取额外回执时间")
			}
			if conn.Exec("UPDATE public.consumer_receipts SET operation_id=operation_id").Error == nil {
				t.Error("后台不得改写回执")
			}
			return nil
		})
		if failure != nil {
			t.Fatal(failure)
		}
		if status(event.CorrelationID) != "cancelled" || scalar("SELECT count(*) FROM public.consumer_receipts") != 1 {
			t.Fatal("实际后台应能无操作终结")
		}
		if failure = migration.Run(ctx, db, log, "down"); failure == nil {
			t.Fatal("非空发件箱不得移除替代防护字段")
		}
		if scalar("SELECT count(*) FROM public.outbox_events WHERE superseded_at IS NOT NULL") != 1 {
			t.Fatal("失败回退不得丢失替代标记")
		}
	})

	t.Run("显式补偿调度取消后停止", func(t *testing.T) {
		clear()
		event := makeWork(1020, "pending")
		bounded, cancel := context.WithCancel(ctx)
		defer cancel()
		schedulerLog := log.WithOptions(zap.Hooks(func(entry zapcore.Entry) error {
			if entry.Message == "数据库工作补偿完成" {
				cancel()
			}
			return nil
		}))
		worker, failure := NewConsumer(db, schedulerLog, definition, c.config)
		if failure != nil {
			t.Fatal(failure)
		}
		if failure = worker.RunCompensator(bounded, time.Millisecond, 10); failure != nil {
			t.Fatal(failure)
		}
		if status(event.CorrelationID) != "completed" {
			t.Fatal("应在首批提交后取消调度")
		}
		if failure = worker.RunCompensator(ctx, 0, 10); failure != ErrInvalid {
			t.Fatal("调度间隔必须显式有效")
		}
	})

	for _, message := range []string{"数据库工作补偿完成", "持久工作重驱动暂存完成"} {
		entries := observed.FilterMessage(message).All()
		if len(entries) == 0 {
			t.Fatal("缺少完成日志")
		}
		for _, entry := range entries {
			fields := entry.ContextMap()
			if fields["duration"] == nil || fields["result"] == nil || strings.Contains(fmt.Sprint(fields), "SYNTHETIC_REDRIVE_TOKEN") {
				t.Fatal("日志字段或脱敏错误")
			}
		}
	}
	if _, failure := c.Redrive(ctx, nil, policy, RedriveRequest{WorkID: draft(1017).WorkID, ExpectedVersion: 5}); !errors.Is(failure, command.ErrInvalid) {
		t.Fatal("无命令事务应拒绝")
	}
}

// T007/REQ-018、019：无中间件也校验有界扫描、显式调度参数和后台授权入口失败关闭。
func TestRecoveryContracts(t *testing.T) {
	ctx := context.Background()
	c := &Consumer{logger: zap.NewNop()}
	if _, err := c.Compensate(ctx, 0); err != ErrInvalid {
		t.Fatal("补偿禁止无界参数")
	}
	if err := c.RunCompensator(ctx, 0, 10); err != ErrInvalid {
		t.Fatal("调度必须显式指定间隔")
	}
	input := RedriveRequest{WorkID: uid(1), ExpectedVersion: 1}
	policy := RedrivePolicy{AllowedRoles: []string{"super_admin"}, Recheck: func(context.Context, *command.Tx, Work) (RedriveDecision, error) { return Resume, nil }}
	if _, err := c.Redrive(ctx, &command.Tx{}, policy, input); err != command.ErrForbidden {
		t.Fatal("后台工作事务不能冒充管理员命令")
	}
	if _, err := c.RedriveCommand(RedrivePolicy{AllowedRoles: policy.AllowedRoles}, input); err != command.ErrInvalid {
		t.Fatal("缺当前复核必须拒绝")
	}
	denied := policy
	denied.AllowedRoles = []string{"client_role"}
	if _, err := c.RedriveCommand(denied, input); err != command.ErrInvalid {
		t.Fatal("任意角色必须拒绝")
	}
	input.ExpectedVersion = 0
	if _, err := c.RedriveCommand(policy, input); err != command.ErrInvalid {
		t.Fatal("缺工作版本必须拒绝")
	}
}
