package adminauth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

// T008/REQ-002、014、016、018、019：真实PG+Redis+HTTP，只有身份资料为合成夹具，不接微信。
func TestPostgresAdmin(t *testing.T) {
	dsn, address := os.Getenv("APP_ADMINAUTH_TEST_DSN"), os.Getenv("APP_ADMINAUTH_TEST_REDIS")
	if dsn == "" || address == "" {
		t.Skip("需隔离真实PostgreSQL与Redis：python3 scripts/check-adminauth.py")
	}
	ctx := context.Background()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal("数据库开启失败")
	}
	pool, _ := db.DB()
	defer pool.Close()
	core, observed := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	if err = migration.Run(ctx, db, logger, "up"); err != nil {
		t.Fatal(err)
	}
	if err = migration.ApplyPermissions(ctx, db, logger); err != nil {
		t.Fatal(err)
	}
	// 有效用户为维护组而非superuser；每个连接都用同一启动参数，不依赖连接池碰巧复用SET ROLE。
	restricted, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn + " options='-c role=liyuanci_admin_maintenance'", PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal("窄权限连接失败")
	}
	restrictedPool, _ := restricted.DB()
	defer restrictedPool.Close()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	limiter, err := adminauth.NewRedisLimiter(client, time.Minute, 1000)
	if err != nil {
		t.Fatal(err)
	}
	cfg := adminauth.Config{
		SessionTTL:      time.Hour,
		InitialPassword: "123456",
		ReceiptKey:      []byte(strings.Repeat("SYNTHETIC_KEY_", 4)),
		Command:         command.Config{Timeout: 20 * time.Second, LockTimeout: 5 * time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3},
	}
	// 装配层拒绝真实超级用户，即使通过启动参数切换成受限role，也不能伪装独立连接。
	passwordPath, keyPath := filepath.Join(t.TempDir(), "initial"), filepath.Join(t.TempDir(), "key")
	if e := os.WriteFile(passwordPath, []byte(cfg.InitialPassword), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(keyPath, cfg.ReceiptKey, 0600); e != nil {
		t.Fatal(e)
	}
	settings := config.Config{}
	settings.Database.MaxOpen = 4
	settings.Database.MaxIdle = 2
	settings.Database.MaxLifetime = time.Minute
	settings.AdminAuth.Enabled = true
	settings.AdminAuth.DSN = dsn
	settings.AdminAuth.InitialPasswordFile = passwordPath
	settings.AdminAuth.ReceiptKeyFile = keyPath
	settings.AdminAuth.SessionTTL = cfg.SessionTTL
	settings.AdminAuth.Timeout = cfg.Command.Timeout
	settings.AdminAuth.LockTimeout = cfg.Command.LockTimeout
	settings.AdminAuth.ReceiptTTL = cfg.Command.ReceiptTTL
	settings.AdminAuth.MaxAttempts = cfg.Command.MaxAttempts
	settings.AdminAuth.LoginWindow = time.Minute
	settings.AdminAuth.LoginLimit = 1000
	if _, _, e := adminauth.Open(ctx, settings, client, logger); e != adminauth.ErrUnavailable {
		t.Fatal("超级用户身份不得伪装独立连接", e)
	}
	settings.AdminAuth.DSN = dsn + " options='-c role=liyuanci_admin_maintenance'"
	if _, _, e := adminauth.Open(ctx, settings, client, logger); e != adminauth.ErrUnavailable {
		t.Fatal("切换role不能掩盖原始超级用户身份", e)
	}
	var random [24]byte
	if _, e := rand.Read(random[:]); e != nil {
		t.Fatal(e)
	}
	databasePassword := hex.EncodeToString(random[:])
	if e := db.Exec("CREATE ROLE adminauth_fixture LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD '" + databasePassword + "' IN ROLE liyuanci_admin_maintenance").Error; e != nil {
		t.Fatal("隔离运行身份创建失败")
	}
	fixtureDSN := strings.Replace(dsn, "user=postgres", "user=adminauth_fixture", 1)
	// 本工具DSN中密码是随机十六进制；不输出替换前后的连接串。
	startAt := strings.Index(fixtureDSN, "password=")
	endAt := strings.Index(fixtureDSN[startAt:], " ") + startAt
	if startAt < 0 || endAt < startAt {
		t.Fatal("隔离DSN形状无效")
	}
	settings.AdminAuth.DSN = fixtureDSN[:startAt] + "password=" + databasePassword + fixtureDSN[endAt:]
	opened, closeOpened, e := adminauth.Open(ctx, settings, client, logger)
	if e != nil {
		t.Fatal("显式装配失败", e)
	}
	defer closeOpened()
	_ = opened
	service, err := adminauth.New(restricted, limiter, logger, cfg)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if e := db.Exec(sql, args...).Error; e != nil {
			t.Fatal(e)
		}
	}
	root, err := service.Bootstrap(ctx, "synthetic-root")
	if err != nil || len(root.Roles) != 1 || root.Roles[0] != "super_admin" {
		t.Fatal("离线初始化失败", err)
	}
	if _, err = service.Bootstrap(ctx, "other-root"); err != adminauth.ErrConflict {
		t.Fatal("重复初始化不能接管或重置", err)
	}
	if _, err = service.Login(ctx, "synthetic-root", "WRONG_SYNTHETIC", "source-root", "login_failure"); err != adminauth.ErrUnauthenticated {
		t.Fatal("错误口令应统一401", err)
	}
	if _, err = service.Login(ctx, "missing-synthetic", "WRONG_SYNTHETIC", "source-root", "login_unknown"); err != adminauth.ErrUnauthenticated {
		t.Fatal("未知账号不能枚举", err)
	}
	session, err := service.Login(ctx, "synthetic-root", "123456", "source-root", "login_success")
	if err != nil {
		t.Fatal(err)
	}
	var tokenCount int64
	digest := sha256.Sum256([]byte(session.Token))
	if e := db.Raw("SELECT count(*) FROM public.sessions WHERE token_hash=?", digest[:]).Scan(&tokenCount).Error; e != nil || tokenCount != 1 {
		t.Fatal("只保存令牌摘要")
	}
	router := httpapi.New(logger, func(context.Context) error { return nil })
	if err = httpapi.RegisterAdmin(router, service); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
			t.Fatal("敏感缓存或关联头缺失")
		}
		return rec
	}
	body := `{"login_name":"synthetic-review","roles":["review_operator"],"reason":"合成创建"}`
	rec := request("POST", "/api/v1/admin/accounts", session.Token, "create-key", body)
	if rec.Code != 201 || strings.Contains(rec.Body.String(), "password") {
		t.Fatal("超管创建或投影失败", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data adminauth.Account `json:"data"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &envelope) != nil {
		t.Fatal("响应解析失败")
	}
	review := envelope.Data
	repeated := request("POST", "/api/v1/admin/accounts", session.Token, "create-key", body)
	var replay struct {
		Data adminauth.Account `json:"data"`
	}
	_ = json.Unmarshal(repeated.Body.Bytes(), &replay)
	if repeated.Code != 201 || replay.Data.ID != review.ID {
		t.Fatal("同键重放不能新建账号")
	}
	if got := request("POST", "/api/v1/admin/accounts", session.Token, "create-key", strings.Replace(body, "synthetic-review", "different-name", 1)).Code; got != 409 {
		t.Fatal("异摘要应409", got)
	}
	if got := request("POST", "/api/v1/admin/accounts", session.Token, "duplicate-name", body).Code; got != 409 {
		t.Fatal("重复名称应409", got)
	}
	operator, err := service.Login(ctx, "synthetic-review", "123456", "source-review", "login_review")
	if err != nil {
		t.Fatal(err)
	}

	// T008/REQ-018、019：角色管理与分页使用真实受限数据库身份；不进行跨端联调。
	t.Run("账号列表与角色修改", func(t *testing.T) {
		page, e := service.ListAccounts(ctx, session.Token, "", "accounts_first", 1)
		if e != nil || len(page.Items) != 1 || page.NextCursor == nil {
			t.Fatal("首分页失败", e)
		}
		second, e := service.ListAccounts(ctx, session.Token, *page.NextCursor, "accounts_next", 1)
		if e != nil || len(second.Items) != 1 || second.Items[0].ID == page.Items[0].ID {
			t.Fatal("键集分页重复", e)
		}
		if _, e = service.ListAccounts(ctx, session.Token, *page.NextCursor+"X", "accounts_tampered", 1); e != adminauth.ErrInvalid {
			t.Fatal("篡改游标应拒绝", e)
		}
		if _, e = service.ListAccounts(ctx, operator.Token, "", "accounts_forbidden", 20); e != adminauth.ErrForbidden {
			t.Fatal("普通运营不可列账号", e)
		}
		local, e := service.Create(ctx, session.Token, "roles_fixture", "roles_create", adminauth.CreateInput{LoginName: "roles-fixture", Roles: []string{}, Reason: "合成角色测试"})
		if e != nil {
			t.Fatal(e)
		}
		input := adminauth.RolesInput{Roles: []string{"review_operator"}, Reason: "合成授权", ExpectedVersion: local.Version}
		if _, e = service.ChangeRoles(ctx, operator.Token, "roles_forbidden", "roles_forbidden", local.ID, input); e != adminauth.ErrForbidden {
			t.Fatal("运营不能提权", e)
		}
		changed, e := service.ChangeRoles(ctx, session.Token, "roles_success", "roles_success", local.ID, input)
		if e != nil || changed.Version != local.Version+1 || len(changed.Roles) != 1 {
			t.Fatal("角色替换失败", e)
		}
		replay, e := service.ChangeRoles(ctx, session.Token, "roles_success", "roles_replay", local.ID, input)
		if e != nil || replay.Version != changed.Version {
			t.Fatal("重放不能再次写入", e)
		}
		if _, e = service.ChangeRoles(ctx, session.Token, "roles_stale", "roles_stale", local.ID, input); e != command.ErrVersionConflict {
			t.Fatal("旧版本应冲突", e)
		}
		input.Reason = "不同理由"
		if _, e = service.ChangeRoles(ctx, session.Token, "roles_success", "roles_digest", local.ID, input); e != command.ErrIdempotencyConflict {
			t.Fatal("异摘要应冲突", e)
		}
		// 留迹写入失败时，角色与版本必须原子回滚。
		exec("CREATE FUNCTION public.fail_roles_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action_kind='roles_change' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$")
		exec("CREATE TRIGGER fail_roles_audit BEFORE INSERT ON public.admin_account_actions FOR EACH ROW EXECUTE FUNCTION public.fail_roles_audit()")
		input = adminauth.RolesInput{Roles: []string{}, Reason: "合成回滚", ExpectedVersion: changed.Version}
		_, e = service.ChangeRoles(ctx, session.Token, "roles_rollback", "roles_rollback", local.ID, input)
		exec("DROP TRIGGER fail_roles_audit ON public.admin_account_actions")
		exec("DROP FUNCTION public.fail_roles_audit()")
		if e != adminauth.ErrUnavailable {
			t.Fatal("留迹失败应拒绝", e)
		}
		current, e := service.ListAccounts(ctx, session.Token, "", "roles_rollback_check", 50)
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range current.Items {
			if a.ID == local.ID && (a.Version != changed.Version || len(a.Roles) != 1) {
				t.Fatal("角色变更未回滚")
			}
		}
		// 并发同版本只有一个成功；不使用缓存角色决定权限。
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, e := service.ChangeRoles(ctx, session.Token, fmt.Sprintf("roles_race_%d", i), "roles_race", local.ID, input)
				results <- e
			}(i)
		}
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for e := range results {
			if e == nil {
				success++
			} else if e == command.ErrVersionConflict {
				conflict++
			} else {
				t.Fatal(e)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatal("并发版本保护失败", success, conflict)
		}
		// 一个单独超管撤掉自身权限后，同键成功回执也不能恢复原授权。
		super, e := service.Create(ctx, session.Token, "roles_super", "roles_super", adminauth.CreateInput{LoginName: "roles-super", Roles: []string{"super_admin"}, Reason: "合成超管"})
		if e != nil {
			t.Fatal(e)
		}
		logged, e := service.Login(ctx, "roles-super", "123456", "roles-source", "roles_login")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = service.ListAccounts(ctx, logged.Token, *page.NextCursor, "cursor_cross_actor", 1); e != adminauth.ErrInvalid {
			t.Fatal("跨主体游标应拒绝", e)
		}
		self := adminauth.RolesInput{Roles: []string{}, Reason: "合成撤权", ExpectedVersion: super.Version}
		if _, e = service.ChangeRoles(ctx, logged.Token, "self_revoke", "self_revoke", super.ID, self); e != nil {
			t.Fatal(e)
		}
		if _, e = service.ChangeRoles(ctx, logged.Token, "self_revoke", "self_replay", super.ID, self); e != adminauth.ErrForbidden {
			t.Fatal("撤权后回执不可越权", e)
		}
	})
	if got := request("POST", "/api/v1/admin/accounts", operator.Token, "forbidden", strings.Replace(body, "synthetic-review", "forbidden-name", 1)).Code; got != 403 {
		t.Fatal("普通角色不得创建", got)
	}
	if got := request("POST", "/api/v1/admin/accounts", "bad-token", "bad", body).Code; got != 401 {
		t.Fatal("无效会话应401", got)
	}
	// 回执重放也查当前角色，撤权不能靠成功回执绕过。
	exec("DELETE FROM public.admin_role_bindings WHERE admin_id=?", root.ID)
	if got := request("POST", "/api/v1/admin/accounts", session.Token, "create-key", body).Code; got != 403 {
		t.Fatal("撤权重放应403", got)
	}
	exec("INSERT INTO public.admin_role_bindings(admin_id,role) VALUES (?,'super_admin')", root.ID)
	// 两并发同键只建立一个账号；真实guard和回执承担串行化。
	var group sync.WaitGroup
	results := make(chan adminauth.Account, 2)
	failures := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			account, e := service.Create(ctx, session.Token, "concurrent_key", "concurrent", adminauth.CreateInput{LoginName: "concurrent-name", Roles: []string{"review_operator"}, Reason: "合成并发"})
			results <- account
			failures <- e
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal("并发创建失败", e)
		}
	}
	id := ""
	for account := range results {
		if id != "" && id != account.ID {
			t.Fatal("并发重复创建")
		}
		id = account.ID
	}
	// 服务身份不能越过会话命名空间，也不能改写/删除审计。
	exec("INSERT INTO public.accounts(id,nickname) VALUES ('90000000-0000-4000-8000-000000000002','合成学生')")
	if restricted.Exec("DELETE FROM public.admin_account_actions").Error == nil {
		t.Fatal("运行组不能删除留迹")
	}
	if restricted.Exec("UPDATE public.admin_account_actions SET reason='unexpected'").Error == nil {
		t.Fatal("运行组不能篡改留迹")
	}
	if restricted.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES ('90000000-0000-4000-8000-000000000001','90000000-0000-4000-8000-000000000002',decode(repeat('ab',32),'hex'),1,clock_timestamp()+interval '1 hour')").Error == nil {
		t.Fatal("后台组不能建立学生会话")
	}
	second, err := service.Login(ctx, "synthetic-review", "123456", "source-review", "login_second")
	if err != nil {
		t.Fatal(err)
	}
	wrong := request("POST", "/api/v1/admin/password", operator.Token, "wrong-password", `{"current_password":"wrong","new_password":"SYNTHETIC_NEW"}`)
	if wrong.Code != 403 {
		t.Fatal("旧口令错误应403", wrong.Code)
	}
	// 注入留迹故障，验证口令/权限代次/会话撤销和回执全部回滚。
	exec("CREATE FUNCTION public.reject_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'SYNTHETIC_FAULT'; END $$")
	exec("CREATE TRIGGER reject_password_audit BEFORE INSERT ON public.admin_account_actions FOR EACH ROW WHEN (NEW.action_kind='password_change') EXECUTE FUNCTION public.reject_admin_audit()")
	if e := service.ChangePassword(ctx, operator.Token, "rollback", "rollback", adminauth.PasswordInput{CurrentPassword: "123456", NewPassword: "SYNTHETIC_ABORT"}); e != adminauth.ErrUnavailable {
		t.Fatal("留迹失败应回滚", e)
	}
	exec("DROP TRIGGER reject_password_audit ON public.admin_account_actions")
	exec("DROP FUNCTION public.reject_admin_audit()")
	if _, e := service.Login(ctx, "synthetic-review", "123456", "source-review", "after_rollback"); e != nil {
		t.Fatal("回滚不能改变口令", e)
	}
	good := request("POST", "/api/v1/admin/password", operator.Token, "change-password", `{"current_password":"123456","new_password":"SYNTHETIC_NEW"}`)
	if good.Code != 204 || good.Body.Len() != 0 {
		t.Fatal("改密必须204空体", good.Code, good.Body.String())
	}
	for _, token := range []string{operator.Token, second.Token} {
		if got := request("POST", "/api/v1/admin/password", token, "old-session", `{"current_password":"123456","new_password":"SYNTHETIC_NEW"}`).Code; got != 401 {
			t.Fatal("所有旧会话必须撤销", got)
		}
	}
	if _, e := service.Login(ctx, "synthetic-review", "123456", "source-review", "old_password"); e != adminauth.ErrUnauthenticated {
		t.Fatal("旧口令不可再登录", e)
	}
	updated, e := service.Login(ctx, "synthetic-review", "SYNTHETIC_NEW", "source-review", "new_password")
	if e != nil {
		t.Fatal(e)
	}
	// 主体禁用、代次变化及绝对过期都即时失效，学生token不成为后台身份。
	exec("UPDATE public.admin_accounts SET status='disabled' WHERE id=?", review.ID)
	if e = service.Logout(ctx, updated.Token, "disabled", "disabled"); e != adminauth.ErrUnauthenticated {
		t.Fatal("禁用必须失效", e)
	}
	exec("UPDATE public.admin_accounts SET status='active' WHERE id=?", review.ID)
	exec("UPDATE public.sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE admin_id=?", review.ID)
	if e = service.Logout(ctx, updated.Token, "expired", "expired"); e != adminauth.ErrUnauthenticated {
		t.Fatal("到期必须失效", e)
	}
	fresh, e := service.Login(ctx, "synthetic-review", "SYNTHETIC_NEW", "source-review", "fresh")
	if e != nil {
		t.Fatal(e)
	}
	if got := request("DELETE", "/api/v1/admin/session", fresh.Token, "logout", "").Code; got != 204 {
		t.Fatal("退出应204", got)
	}
	if e = service.Logout(ctx, fresh.Token, "logout_again", "logout_again"); e != adminauth.ErrUnauthenticated {
		t.Fatal("退出后token失效", e)
	}
	// 已存在有效会话的权限代次变化立即生效。
	epochSession, e := service.Login(ctx, "synthetic-review", "SYNTHETIC_NEW", "source-review", "epoch_login")
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE public.admin_accounts SET auth_epoch=auth_epoch+1 WHERE id=?", review.ID)
	if e = service.Logout(ctx, epochSession.Token, "epoch", "epoch"); e != adminauth.ErrUnauthenticated {
		t.Fatal("代次变化必须失效", e)
	}
	empty, e := service.Create(ctx, session.Token, "empty_roles", "empty_roles", adminauth.CreateInput{LoginName: "no-role", Roles: []string{}, Reason: "合成无角色账号"})
	if e != nil {
		t.Fatal(e)
	}
	roleless, e := service.Login(ctx, "no-role", "123456", "source-empty", "roleless_login")
	if e != nil {
		t.Fatal(e)
	}
	if e = service.ChangePassword(ctx, roleless.Token, "roleless_change", "roleless_change", adminauth.PasswordInput{CurrentPassword: "123456", NewPassword: "SYNTHETIC_ROLELESS"}); e != nil {
		t.Fatal("无角色仍可改本人密码", e)
	}
	// 两个旧会话并发改密，只能提交一次，后来持锁者不得覆盖已变更的代次。
	raceA, e := service.Login(ctx, "no-role", "SYNTHETIC_ROLELESS", "source-empty", "race_a")
	if e != nil {
		t.Fatal(e)
	}
	raceB, e := service.Login(ctx, "no-role", "SYNTHETIC_ROLELESS", "source-empty", "race_b")
	if e != nil {
		t.Fatal(e)
	}
	races := make(chan error, 2)
	for _, token := range []string{raceA.Token, raceB.Token} {
		group.Add(1)
		go func(token string) {
			defer group.Done()
			races <- service.ChangePassword(ctx, token, "same_race_key", "change_race", adminauth.PasswordInput{CurrentPassword: "SYNTHETIC_ROLELESS", NewPassword: "SYNTHETIC_AFTER_RACE"})
		}(token)
	}
	group.Wait()
	close(races)
	successes, rejected := 0, 0
	for e := range races {
		if e == nil {
			successes++
		} else if e == adminauth.ErrUnauthenticated {
			rejected++
		} else {
			t.Fatal("并发改密返回非预期结果", e)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatal("并发改密必须一成一拒")
	}
	var localHash string
	if e = db.Raw("SELECT password_hash FROM public.admin_accounts WHERE id=?", empty.ID).Scan(&localHash).Error; e != nil || strings.Contains(localHash, "SYNTHETIC_AFTER_RACE") {
		t.Fatal("数据库不得存口令原文")
	}

	studentToken := "SYNTHETIC_STUDENT_TOKEN"
	studentHash := sha256.Sum256([]byte(studentToken))
	exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES ('90000000-0000-4000-8000-000000000003','90000000-0000-4000-8000-000000000002',?,1,clock_timestamp()+interval '1 hour')", studentHash[:])
	if e = service.Logout(ctx, studentToken, "student", "student"); e != adminauth.ErrUnauthenticated {
		t.Fatal("学生令牌不能冒充后台会话", e)
	}
	// 共享Redis真实计数、过期和故障拒绝；用独立技术键，不依赖固定sleep。
	small, e := adminauth.NewRedisLimiter(client, time.Minute, 1)
	if e != nil {
		t.Fatal(e)
	}
	if allowed, e := small.Allow(ctx, "synthetic-limit"); e != nil || !allowed {
		t.Fatal("首次限流应允许")
	}
	if allowed, e := small.Allow(ctx, "synthetic-limit"); e != nil || allowed {
		t.Fatal("超限应拒绝")
	}
	if ttl := client.PTTL(ctx, "admin-login:synthetic-limit").Val(); ttl <= 0 {
		t.Fatal("计数必须同事务有过期")
	}
	if e = client.Del(ctx, "admin-login:synthetic-limit").Err(); e != nil {
		t.Fatal(e)
	}
	if allowed, e := small.Allow(ctx, "synthetic-limit"); e != nil || !allowed {
		t.Fatal("窗口重建后允许")
	}
	keys, _ := client.Keys(ctx, "admin-login:*").Result()
	for _, key := range keys {
		if strings.Contains(key, "synthetic-root") || strings.Contains(key, "127.0.0.1") {
			t.Fatal("共享键不得保存身份原文")
		}
	}
	// 本地协议失败和存储故障都不回显输入，完成日志在失败/恢复路径保留。
	for _, entry := range observed.All() {
		if strings.Contains(entry.Message+fmt.Sprint(entry.ContextMap()), "SYNTHETIC_") || strings.Contains(fmt.Sprint(entry.ContextMap()), session.Token) {
			t.Fatal("日志泄漏凭据或故障原文")
		}
	}
	if observed.FilterMessage("后台身份操作完成").Len() < 15 || observed.FilterMessage("HTTP请求处理完成").Len() < 10 {
		t.Fatal("缺少完成日志")
	}
	if err = migration.Run(ctx, db, logger, "down"); err == nil {
		t.Fatal("非空本地账号必须拒绝回退")
	}
	_ = client.Close()
	if _, e = service.Login(ctx, "synthetic-root", "123456", "source-root", "redis_failure"); !errors.Is(e, adminauth.ErrUnavailable) {
		t.Fatal("Redis故障不得放行", e)
	}
	t.Log("真实后台账号/角色/会话/口令/幂等/窄权限/回滚/Redis及HTTP通过；TTL仅合成测试配置，不代表生产选择")
}
