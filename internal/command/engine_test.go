package command

import (
	"bytes"
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
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

const actorID = "10000000-0000-0000-0000-000000000001"
const otherID = "00000000-0000-0000-0000-000000000002"
const adminID = "20000000-0000-0000-0000-000000000001"

func config() Config {
	return Config{
		Timeout:     5 * time.Second,
		LockTimeout: 100 * time.Millisecond,
		ReceiptTTL:  time.Hour,
		MaxAttempts: 3,
	}
}

func definition() Definition {
	return Definition{
		Kind: "account.update", Method: "PATCH", ResourcePath: "/accounts/me", ActorKind: "student", Access: Verified,
		RequiredExpectations: []string{"expected_version"},
		Discover:             func(context.Context, *gorm.DB, Subject) (Discovery, error) { return Discovery{}, nil },
		Authorize:            func(context.Context, *Tx) error { return nil },
		Apply: func(ctx context.Context, tx *Tx) (string, error) {
			return actorID, tx.DB().WithContext(ctx).Exec("UPDATE public.accounts SET version=version+1 WHERE id=?", actorID).Error
		},
		Resolve: func(_ context.Context, tx *Tx, ref string) error {
			if ref != actorID || tx.Actor().ID == "" {
				return ErrNotFound
			}
			return nil
		},
	}
}
func input(key string) Input {
	return Input{Token: "SYNTHETIC_TOKEN", IdempotencyKey: key, RequestID: "request-1", Request: map[string]any{"value": "SYNTHETIC_BODY"}, Expectations: []Expectation{{Name: "expected_version", Target: Resource{Kind: Account, ID: actorID}, Value: 1}}}
}

// T006/REQ-004、007、018：策略和并发字段由服务端确定，摘要不依赖输入集合顺序。
func TestCommandValidation(t *testing.T) {
	d := definition()
	in := input("key")
	if !validDefinition(d) {
		t.Fatal("有效策略被拒绝")
	}
	first, err := requestHash(d, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := requestHash(d, in)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("摘要不稳定")
	}
	in.Expectations = nil
	if _, err = requestHash(d, in); err != ErrInvalid {
		t.Fatal("缺失必填版本应拒绝")
	}
	in = input("key")
	in.Expectations[0].Target.Kind = Conversation
	if _, err = requestHash(d, in); err != ErrInvalid {
		t.Fatal("会话代次不能混用版本")
	}
	d.ResourcePath = "/accounts/../me"
	if validDefinition(d) {
		t.Fatal("非规范路径")
	}
	d = definition()
	d.RequiredExpectations = append(d.RequiredExpectations, "expected_version")
	if validDefinition(d) {
		t.Fatal("重复策略")
	}
	found, err := normalize(Discovery{Accounts: []string{actorID, otherID, actorID}, Resources: []Resource{{Kind: Message, ID: actorID}, {Kind: Wish, ID: actorID}, {Kind: Wish, ID: actorID}}}, Subject{Kind: "student", ID: actorID})
	if err != nil || len(found.Accounts) != 2 || found.Accounts[0] != otherID || len(found.Resources) != 2 || found.Resources[0].Kind != Wish {
		t.Fatal("锁序未排序去重")
	}
	if _, err = normalize(Discovery{Resources: []Resource{{Kind: Account, ID: actorID}}}, Subject{Kind: "student", ID: actorID}); err != ErrInvalid {
		t.Fatal("账号不可重复作为资源")
	}
	if _, err = New(nil, zap.NewNop(), config()); err != ErrInvalid {
		t.Fatal("缺失数据库")
	}
	if _, err = New(&gorm.DB{}, nil, config()); err != ErrInvalid {
		t.Fatal("缺失日志器")
	}
}

type sqlFault string

func (s sqlFault) Error() string    { return "SYNTHETIC_DATABASE_SECRET" }
func (s sqlFault) SQLState() string { return string(s) }

// T006/REQ-018：数据库原文不越过命令错误边界；重试不扩张到未知错误。
func TestErrorBoundary(t *testing.T) {
	for state, want := range map[sqlFault]error{"40001": ErrRetry, "40P01": ErrRetry, "55P03": ErrRetry, "23505": ErrConflict, "25006": ErrInternal, "XX000": ErrUnavailable} {
		if got := normalizeError(fmt.Errorf("wrapped: %w", state)); got != want {
			t.Fatal(state, got)
		}
	}
	if normalizeError(context.Canceled) != context.Canceled || normalizeError(errors.New("secret")) != ErrUnavailable {
		t.Fatal("错误映射")
	}
}

// T006/REQ-004、005、006、007、014、016、018：专用 Docker PostgreSQL，全部身份及输入均为合成夹具。
func TestPostgresCommands(t *testing.T) {
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
	core, observed := observer.New(zap.InfoLevel)
	log := zap.New(core)
	if err = migration.Run(ctx, db, log, "up"); err != nil {
		t.Fatal(err)
	}
	if err = migration.ApplyPermissions(ctx, db, log); err != nil {
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
	exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'合成账号','verified'),(?,'合成参与者','verified')", actorID, otherID)
	hash := sha256.Sum256([]byte("SYNTHETIC_TOKEN"))
	adminHash := sha256.Sum256([]byte("SYNTHETIC_ADMIN_TOKEN"))
	exec("INSERT INTO public.admin_accounts(id,credential_ref) VALUES (?,'synthetic-provider')", adminID)
	exec("INSERT INTO public.admin_role_bindings(admin_id,role) VALUES (?,'review_operator')", adminID)
	exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", "30000000-0000-0000-0000-000000000001", actorID, hash[:])
	exec("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", "30000000-0000-0000-0000-000000000002", adminID, adminHash[:])
	engine, err := New(db, log, config())
	if err != nil {
		t.Fatal(err)
	}
	version := func() int64 {
		var n int64
		if failure := db.Raw("SELECT version FROM public.accounts WHERE id=?", actorID).Scan(&n).Error; failure != nil {
			t.Fatal(failure)
		}
		return n
	}
	run := func(d Definition, in Input, want error) Outcome {
		t.Helper()
		out, failure := engine.Execute(ctx, d, in)
		if !errors.Is(failure, want) {
			t.Fatalf("命令错误 got=%v want=%v", failure, want)
		}
		return out
	}
	reset := func() {
		exec("DELETE FROM public.command_receipts")
		exec("DELETE FROM public.penalties")
		exec("UPDATE public.accounts SET version=1,auth_epoch=1,verification='verified',lifecycle='active'")
		exec("UPDATE public.sessions SET auth_epoch=1,revoked_at=NULL,expires_at=clock_timestamp()+interval '1 hour'")
	}
	t.Run("并发幂等与重放", func(t *testing.T) {
		var calls atomic.Int32
		d := definition()
		apply := d.Apply
		d.Apply = func(ctx context.Context, tx *Tx) (string, error) { calls.Add(1); return apply(ctx, tx) }
		var wg sync.WaitGroup
		failures := make(chan error, 8)
		replays := make(chan bool, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, failure := engine.Execute(ctx, d, input("same-key"))
				failures <- failure
				replays <- out.Replayed
			}()
		}
		wg.Wait()
		close(failures)
		close(replays)
		for failure := range failures {
			if failure != nil {
				t.Fatal(failure)
			}
		}
		count := 0
		for replay := range replays {
			if replay {
				count++
			}
		}
		if calls.Load() != 1 || version() != 2 || count != 7 {
			t.Fatal("相同命令只能提交一次")
		}
		changed := input("same-key")
		changed.Request = map[string]any{"value": "different"}
		run(d, changed, ErrIdempotencyConflict)
		changed = input("same-key")
		changed.Expectations[0].Value = 2
		run(d, changed, ErrIdempotencyConflict)
		d.Resolve = func(context.Context, *Tx, string) error { return ErrNotFound }
		run(d, input("same-key"), ErrNotFound)
	})
	t.Run("回滚与恢复日志", func(t *testing.T) {
		reset()
		for _, panicNow := range []bool{false, true} {
			d := definition()
			d.Apply = func(ctx context.Context, tx *Tx) (string, error) {
				if failure := tx.DB().WithContext(ctx).Exec("UPDATE public.accounts SET version=version+1 WHERE id=?", actorID).Error; failure != nil {
					return "", failure
				}
				if panicNow {
					panic("SYNTHETIC_PANIC_SECRET")
				}
				return "", ErrConflict
			}
			want := ErrConflict
			if panicNow {
				want = ErrInternal
			}
			out := run(d, input("rollback"), want)
			if version() != 1 || out.Ref != "" {
				t.Fatal("失败必须回滚且无引用")
			}
		}
		var count int64
		db.Raw("SELECT count(*) FROM public.command_receipts").Scan(&count)
		if count != 0 {
			t.Fatal("失败不能持久化回执")
		}
	})
	t.Run("当前授权复核", func(t *testing.T) {
		for _, tc := range []struct {
			name, sql string
			want      error
		}{
			{"代次", "UPDATE public.accounts SET auth_epoch=2", ErrUnauthenticated},
			{"撤销", "UPDATE public.sessions SET revoked_at=clock_timestamp()", ErrUnauthenticated},
			{"过期", "UPDATE public.sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour'", ErrUnauthenticated},
			{"注销", "UPDATE public.accounts SET lifecycle='deleting'", ErrUnauthenticated},
			{"资格", "UPDATE public.accounts SET verification='rejected'", ErrForbidden},
		} {
			t.Run(tc.name, func(t *testing.T) {
				reset()
				run(definition(), input("auth-replay"), nil)
				exec(tc.sql)
				run(definition(), input("auth-replay"), tc.want)
			})
		}
		for _, action := range []string{"warning", "mute", "ban"} {
			reset()
			level := "minor"
			ends := "NULL"
			if action == "mute" {
				level = "general"
				ends = "clock_timestamp()+interval '168 hours'"
			}
			if action == "ban" {
				level = "severe"
			}
			// 单条语句用同一时间表达式，满足精确七天约束。
			starts := "CURRENT_TIMESTAMP"
			ends = strings.ReplaceAll(ends, "clock_timestamp()", starts)
			exec("INSERT INTO public.penalties(id,account_id,level,action_kind,violation_ref,content_version,starts_at,ends_at,reason_ref) VALUES (?,?,?, ?,?,1,"+starts+","+ends+",?)", "40000000-0000-0000-0000-000000000001", actorID, level, action, otherID, otherID)
			want := ErrForbidden
			if action == "warning" {
				want = nil
			}
			run(definition(), input("penalty"), want)
			if action != "warning" {
				d := definition()
				d.Access = Restricted
				run(d, input("restricted"), nil)
			}
		}
	})

	t.Run("数据库参与者变化重新发现", func(t *testing.T) {
		reset()
		discovered := make(chan struct{})
		changed := make(chan error, 1)
		// T006/REQ-004、005：另一事务遵守账号锁序，在只读发现后增加更小 UUID 参与者。
		go func() {
			select {
			case <-discovered:
			case <-time.After(5 * time.Second):
				changed <- errors.New("未进入发现阶段")
				return
			}
			changed <- db.Transaction(func(writer *gorm.DB) error {
				if failure := writer.Exec("SELECT id FROM public.accounts WHERE id IN (?,?) ORDER BY id FOR UPDATE", actorID, otherID).Error; failure != nil {
					return failure
				}
				return writer.Exec("INSERT INTO public.blocks(owner_id,target_id) VALUES (?,?)", actorID, otherID).Error
			})
		}()
		defer exec("DELETE FROM public.blocks WHERE owner_id=?", actorID)
		var discoveries atomic.Int32
		d := definition()
		d.Discover = func(ctx context.Context, reader *gorm.DB, _ Subject) (Discovery, error) {
			var participants []string
			if failure := reader.WithContext(ctx).Raw("SELECT target_id FROM public.blocks WHERE owner_id=? ORDER BY target_id", actorID).Scan(&participants).Error; failure != nil {
				return Discovery{}, failure
			}
			if discoveries.Add(1) == 1 {
				close(discovered)
				select {
				case failure := <-changed:
					if failure != nil {
						return Discovery{}, failure
					}
				case <-ctx.Done():
					return Discovery{}, ctx.Err()
				}
			}
			return Discovery{Accounts: participants}, nil
		}
		apply := d.Apply
		d.Apply = func(ctx context.Context, tx *Tx) (string, error) {
			if _, present := tx.Guard(otherID); !present {
				return "", ErrInternal
			}
			return apply(ctx, tx)
		}
		out := run(d, input("rediscover"), nil)
		if out.Attempts != 2 || version() != 2 {
			t.Fatal("数据库集合改变后必须回滚并重新按序锁定")
		}
	})
	t.Run("持锁前撤销必须再次授权", func(t *testing.T) {
		reset()
		discovered := make(chan struct{})
		revoked := make(chan error, 1)
		// T006/REQ-014、016、018：会话初读有效，撤销事务先取主体 guard，再撤销会话。
		go func() {
			select {
			case <-discovered:
			case <-time.After(5 * time.Second):
				revoked <- errors.New("未进入发现阶段")
				return
			}
			revoked <- db.Transaction(func(writer *gorm.DB) error {
				if failure := writer.Exec("SELECT id FROM public.accounts WHERE id=? FOR UPDATE", actorID).Error; failure != nil {
					return failure
				}
				return writer.Exec("UPDATE public.sessions SET revoked_at=clock_timestamp() WHERE account_id=?", actorID).Error
			})
		}()
		d := definition()
		var discoveries atomic.Int32
		var applies atomic.Int32
		d.Discover = func(ctx context.Context, _ *gorm.DB, _ Subject) (Discovery, error) {
			if discoveries.Add(1) == 1 {
				close(discovered)
				select {
				case failure := <-revoked:
					if failure != nil {
						return Discovery{}, failure
					}
				case <-ctx.Done():
					return Discovery{}, ctx.Err()
				}
			}
			return Discovery{}, nil
		}
		d.Apply = func(context.Context, *Tx) (string, error) { applies.Add(1); return actorID, nil }
		run(d, input("revocation-race"), ErrUnauthenticated)
		var count int64
		if failure := db.Raw("SELECT count(*) FROM public.command_receipts").Scan(&count).Error; failure != nil {
			t.Fatal(failure)
		}
		if applies.Load() != 0 || version() != 1 || count != 0 {
			t.Fatal("旧发现结果不得越过锁后撤销复核")
		}
	})
	t.Run("版本保持与有界重试", func(t *testing.T) {
		reset()
		in := input("version")
		in.Expectations[0].Value = 2
		out := run(definition(), in, ErrVersionConflict)
		if out.Attempts != 3 || version() != 1 {
			t.Fatal("不能刷新客户端期望版本")
		}
		var calls int
		d := definition()
		apply := d.Apply
		d.Apply = func(ctx context.Context, tx *Tx) (string, error) {
			calls++
			if calls < 3 {
				tx.DB().WithContext(ctx).Exec("UPDATE public.accounts SET version=version+1 WHERE id=?", actorID)
				return "", sqlFault("40001")
			}
			return apply(ctx, tx)
		}
		out = run(d, input("serialization"), nil)
		if out.Attempts != 3 || version() != 2 {
			t.Fatal("重试必须回滚前次写入")
		}
	})
	t.Run("只读发现与事务所有权", func(t *testing.T) {
		reset()
		d := definition()
		d.Discover = func(ctx context.Context, reader *gorm.DB, _ Subject) (Discovery, error) {
			return Discovery{}, reader.WithContext(ctx).Exec("UPDATE public.accounts SET version=9").Error
		}
		run(d, input("readonly"), ErrInternal)
		if version() != 1 {
			t.Fatal("发现不得写入")
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			_, failure := New(tx, log, config())
			if failure != ErrInvalid {
				t.Fatal("不能接收外部事务")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("锁超时", func(t *testing.T) {
		reset()
		lock := db.Begin()
		if lock.Error != nil {
			t.Fatal(lock.Error)
		}
		defer lock.Rollback()
		if err := lock.Exec("SELECT id FROM public.accounts WHERE id=? FOR UPDATE", actorID).Error; err != nil {
			t.Fatal(err)
		}
		out := run(definition(), input("timeout"), ErrRetry)
		if out.Attempts != 3 || version() != 1 {
			t.Fatal("锁等待须有界且不写入")
		}
	})
	t.Run("资源锁与独立版本字段", func(t *testing.T) {
		reset()
		wish := "50000000-0000-0000-0000-000000000001"
		application := "50000000-0000-0000-0000-000000000002"
		match := "50000000-0000-0000-0000-000000000003"
		conversation := "50000000-0000-0000-0000-000000000004"
		failure := db.Transaction(func(tx *gorm.DB) error {
			if failure := tx.Exec("INSERT INTO public.wishes(id,owner_id,current_content_version,category,campus) VALUES (?,?,1,'help','any')", wish, actorID).Error; failure != nil {
				return failure
			}
			return tx.Exec("INSERT INTO public.wish_versions(wish_id,content_version,body_cipher,school_detection_version) VALUES (?,1,'{}','synthetic')", wish).Error
		})
		if failure != nil {
			t.Fatal(failure)
		}
		exec("INSERT INTO public.applications(id,wish_id,wish_content_version,applicant_id,message_cipher) VALUES (?,?,1,?,'{}')", application, wish, otherID)
		exec("INSERT INTO public.matches(id,wish_id,requester_id,helper_id) VALUES (?,?,?,?)", match, wish, actorID, otherID)
		exec("INSERT INTO public.conversations(id,match_id) VALUES (?,?)", conversation, match)
		d := definition()
		d.RequiredExpectations = []string{"expected_version", "wish_expected_version", "expected_write_epoch"}
		d.Discover = func(context.Context, *gorm.DB, Subject) (Discovery, error) {
			return Discovery{Accounts: []string{otherID}, Resources: []Resource{{Kind: Conversation, ID: conversation}, {Kind: Match, ID: match}, {Kind: Application, ID: application}, {Kind: Wish, ID: wish}}}, nil
		}
		in := input("resources")
		in.Expectations = []Expectation{
			{Name: "expected_version", Target: Resource{Kind: Application, ID: application}, Value: 1},
			{Name: "wish_expected_version", Target: Resource{Kind: Wish, ID: wish}, Value: 1},
			{Name: "expected_write_epoch", Target: Resource{Kind: Conversation, ID: conversation}, Value: 1},
		}
		run(d, in, nil)
		exec("UPDATE public.conversations SET write_epoch=2 WHERE id=?", conversation)
		in.IdempotencyKey = "stale-epoch"
		run(d, in, ErrVersionConflict)
		in.Expectations[2].Value = 2
		run(d, in, nil)
		d.Discover = func(context.Context, *gorm.DB, Subject) (Discovery, error) {
			return Discovery{Resources: []Resource{{Kind: Wish, ID: otherID}}}, nil
		}
		in.IdempotencyKey = "missing-resource"
		run(d, in, ErrNotFound)
	})
	t.Run("真实运行权限及后台角色", func(t *testing.T) {
		reset()
		exec("GRANT UPDATE (key_hash) ON public.command_receipts TO liyuanci_api")
		exec("GRANT EXECUTE ON FUNCTION public.lock_command_admin(uuid) TO PUBLIC")
		if failure := migration.ApplyPermissions(ctx, db, log); failure != nil {
			t.Fatal(failure)
		}
		asRole := func(role string, fn func(*gorm.DB) error) {
			t.Helper()
			failure := db.Connection(func(conn *gorm.DB) error {
				if failure := conn.Exec("SET ROLE " + role).Error; failure != nil {
					return failure
				}
				defer conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE")
				return fn(conn)
			})
			if failure != nil {
				t.Fatal(failure)
			}
		}
		asRole("liyuanci_api", func(conn *gorm.DB) error {
			service, failure := New(conn, log, config())
			if failure != nil {
				return failure
			}
			_, failure = service.Execute(ctx, definition(), input("role"))
			return failure
		})
		// 过期回执可在同一主体 guard 下重用，不能修改幂等键和命令命名空间。
		exec("UPDATE public.command_receipts SET expires_at=clock_timestamp()-interval '1 second'")
		asRole("liyuanci_api", func(conn *gorm.DB) error {
			service, failure := New(conn, log, config())
			if failure != nil {
				return failure
			}
			in := input("role")
			in.Expectations[0].Value = 2
			_, failure = service.Execute(ctx, definition(), in)
			return failure
		})
		asRole("liyuanci_api", func(conn *gorm.DB) error {
			if conn.Exec("UPDATE public.command_receipts SET key_hash=key_hash").Error == nil {
				return errors.New("禁止修改幂等键")
			}
			if conn.Exec("SELECT * FROM public.lock_command_admin(?)", adminID).Error == nil {
				return errors.New("学生不能调用后台 guard")
			}
			return nil
		})
		d := definition()
		d.ActorKind = "admin"
		d.Access = ""
		d.AllowedRoles = []string{"review_operator"}
		d.RequiredExpectations = nil
		d.Apply = func(context.Context, *Tx) (string, error) { return adminID, nil }
		d.Resolve = func(_ context.Context, tx *Tx, ref string) error {
			if ref != adminID || tx.Actor().Kind != "admin" {
				return ErrForbidden
			}
			return nil
		}
		in := input("admin")
		in.Token = "SYNTHETIC_ADMIN_TOKEN"
		in.Expectations = nil
		asRole("liyuanci_admin_api", func(conn *gorm.DB) error {
			service, failure := New(conn, log, config())
			if failure != nil {
				return failure
			}
			_, failure = service.Execute(ctx, d, in)
			return failure
		})
		exec("DELETE FROM public.admin_role_bindings WHERE admin_id=?", adminID)
		run(d, in, ErrForbidden)
		asRole("liyuanci_admin_api", func(conn *gorm.DB) error {
			if conn.Exec("UPDATE public.admin_accounts SET status='disabled'").Error == nil {
				return errors.New("后台 API 不得修改身份")
			}
			return nil
		})
	})
	// 完成日志不能包含令牌、正文、幂等键、panic 或驱动错误原文。
	commandLogs := observed.FilterMessage("数据库命令完成").All()
	if len(commandLogs) == 0 {
		t.Fatal("缺少完成日志")
	}
	recovered := false
	for _, entry := range commandLogs {
		fields := entry.ContextMap()
		if fields["duration"] == nil || fields["result"] == nil || fields["code"] == nil || fields["request_id"] != "request-1" {
			t.Fatal("日志字段缺失")
		}
		recovered = recovered || fields["recovered"] == true
		text := fmt.Sprint(fields)
		for _, secret := range []string{"SYNTHETIC_TOKEN", "SYNTHETIC_BODY", "SYNTHETIC_PANIC_SECRET", "SYNTHETIC_DATABASE_SECRET", "same-key", actorID} {
			if strings.Contains(text, secret) {
				t.Fatal("日志泄漏")
			}
		}
	}
	if !recovered {
		t.Fatal("未记录 panic 恢复")
	}
}
