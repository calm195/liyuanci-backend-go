package studentauth_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/identity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
)

type keys struct{ unavailable atomic.Bool }

func (k *keys) Wrap(_ context.Context, data []byte) (string, []byte, error) {
	if k.unavailable.Load() {
		return "", nil, errors.New("SYNTHETIC_KMS_SECRET")
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{57}, 32))
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	_, e := rand.Read(nonce)
	if e != nil {
		return "", nil, e
	}
	return "synthetic-wrap", append(nonce, gcm.Seal(nil, nonce, data, nil)...), nil
}
func (k *keys) Unwrap(_ context.Context, ref string, data []byte) ([]byte, error) {
	if k.unavailable.Load() || ref != "synthetic-wrap" || len(data) < 28 {
		return nil, errors.New("SYNTHETIC_KMS_SECRET")
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{57}, 32))
	gcm, _ := cipher.NewGCM(block)
	return gcm.Open(nil, data[:12], data[12:], nil)
}
func (k *keys) Digest(_ context.Context, ref, purpose string, data []byte) ([]byte, error) {
	if k.unavailable.Load() {
		return nil, errors.New("SYNTHETIC_KMS_SECRET")
	}
	material := byte(81)
	if ref == "synthetic-new" {
		material = 82
	}
	return securedata.LookupDigest(bytes.Repeat([]byte{material}, 32), purpose, string(data))
}

type exchange struct {
	calls   atomic.Int64
	failure atomic.Bool
}

func (e *exchange) Exchange(_ context.Context, code string) ([]byte, error) {
	e.calls.Add(1)
	if code == "panic-code" {
		panic("SYNTHETIC_OPENID")
	}
	if e.failure.Load() {
		return nil, errors.New("SYNTHETIC_WECHAT_SECRET")
	}
	if strings.HasPrefix(code, "deleting") {
		return []byte("SYNTHETIC_DELETING_OPENID"), nil
	}
	if strings.HasPrefix(code, "same-") {
		return []byte("SYNTHETIC_SHARED_OPENID"), nil
	}
	return []byte("SYNTHETIC_OPENID_" + code), nil
}

// T008/REQ-002、014、018：真实隔离PG/Redis/HTTP，微信和KMS为明确注入的合成适配器，不跨端联调。
func TestPostgresStudent(t *testing.T) {
	dsn, address := os.Getenv("APP_STUDENTAUTH_TEST_DSN"), os.Getenv("APP_STUDENTAUTH_TEST_REDIS")
	if dsn == "" || address == "" {
		t.Skip("需专用PG/Redis：python3 scripts/check-adminauth.py")
	}
	ctx := context.Background()
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	db, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if e != nil {
		t.Fatal("连接失败")
	}
	pool, _ := db.DB()
	defer pool.Close()
	if e = migration.Run(ctx, db, logger, "up"); e != nil {
		t.Fatal(e)
	}
	if e = migration.ApplyPermissions(ctx, db, logger); e != nil {
		t.Fatal(e)
	}
	if e = migration.ApplyPermissions(ctx, db, logger); e != nil {
		t.Fatal(e)
	}
	// 启动参数强制每个连接使用窄身份，不依赖单次SET ROLE碰巧落在同一个池连接。
	restricted, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn + " options='-c role=liyuanci_identity_maintenance'", PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	rp, _ := restricted.DB()
	defer rp.Close()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	limiter, e := adminauth.NewRedisLimiter(client, time.Minute, 10000)
	if e != nil {
		t.Fatal(e)
	}
	keyService := &keys{}
	codec, _ := securedata.New(keyService, logger)
	store, _ := identity.New(restricted, codec, keyService, logger)
	if e = store.Configure(ctx, "openid", "synthetic-old"); e != nil {
		t.Fatal(e)
	}
	exchanger := &exchange{}
	cfg := studentauth.Config{SessionTTL: time.Hour, ExchangeTTL: time.Minute, SessionKey: bytes.Repeat([]byte{98}, 32), Command: command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}}
	service, e := studentauth.New(restricted, logger, exchanger, store, limiter, cfg)
	if e != nil {
		t.Fatal(e)
	}
	login := func(code string) studentauth.Session {
		t.Helper()
		out, e := service.Login(ctx, code, "request-"+code, "source", "student_login")
		if e != nil {
			t.Fatal("登录失败", e)
		}
		return out
	}
	first := login("same-first")
	if first.Me.Verification != "unverified" || first.Token == "" || len(first.Me.AllowedActions) != 2 {
		t.Fatal("微信身份不能授予学生认证")
	}
	repeat, e := service.Login(ctx, "same-first", "request-same-first", "source", "student_replay")
	if e != nil || repeat.Token != first.Token || exchanger.calls.Load() != 1 {
		t.Fatal("同code去重失败", e)
	}
	if _, e = service.Login(ctx, "same-first", "different-request", "source", "student_conflict"); e != command.ErrIdempotencyConflict {
		t.Fatal("异请求标识应冲突", e)
	}
	second := login("same-second")
	if second.Me.ID != first.Me.ID || second.Token == first.Token {
		t.Fatal("同微信身份应同账号不同会话")
	}
	// 双代状态下旧、新摘要共同查重，多个不同code也只能创建一个对应账号。
	if e = store.BeginRotation(ctx, "openid", "synthetic-new"); e != nil {
		t.Fatal(e)
	}
	if login("same-rotated").Me.ID != first.Me.ID {
		t.Fatal("双代查重穿透")
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, e := service.Login(ctx, fmt.Sprintf("same-concurrent-%d", i), fmt.Sprintf("concurrent-%d", i), "source", "student_race")
			if e != nil {
				failures <- e
			} else {
				ids <- out.Me.ID
			}
		}(i)
	}
	wg.Wait()
	close(ids)
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	for id := range ids {
		if id != first.Me.ID {
			t.Fatal("并发身份创建重复")
		}
	}
	// 同一code的并发领取只产生一次微信交换；处理中无有效回执的请求可要求重新登录。
	beforeCalls := exchanger.calls.Load()
	tokens := make(chan string, 6)
	sameFailures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := service.Login(ctx, "same-one-code", "one-code-request", "source", "student_code_race")
			if e != nil {
				sameFailures <- e
			} else {
				tokens <- out.Token
			}
		}()
	}
	wg.Wait()
	close(tokens)
	close(sameFailures)
	gotToken := ""
	for token := range tokens {
		if gotToken != "" && gotToken != token {
			t.Fatal("同code产生不同token")
		}
		gotToken = token
	}
	for e := range sameFailures {
		if e != command.ErrUnauthenticated {
			t.Fatal(e)
		}
	}
	if gotToken == "" || exchanger.calls.Load() != beforeCalls+1 {
		t.Fatal("同code并发未去重")
	}
	var count int64
	hash := sha256.Sum256([]byte(first.Token))
	if e = db.Raw("SELECT count(*) FROM public.sessions WHERE token_hash=?", hash[:]).Scan(&count).Error; e != nil || count != 1 {
		t.Fatal("只存会话摘要")
	}
	hide := true
	input := studentauth.ProfileInput{Nickname: "合成昵称", HideIdentityTags: &hide, ExpectedVersion: first.Me.Version}
	updated, e := service.UpdateProfile(ctx, first.Token, "profile-key", "student_profile", input)
	if e != nil || updated.Version != 2 || !updated.HideIdentityTags {
		t.Fatal("资料更新失败", e)
	}
	replay, e := service.UpdateProfile(ctx, first.Token, "profile-key", "student_profile_replay", input)
	if e != nil || replay.Version != 2 {
		t.Fatal("资料重放失败", e)
	}
	if _, e = service.UpdateProfile(ctx, first.Token, "profile-stale", "student_profile_stale", input); e != command.ErrVersionConflict {
		t.Fatal("资料版本保护", e)
	}
	// HTTP拒绝客户端OpenID/认证状态字段，客户端必须提供wx.login产生的code。
	router := httpapi.New(logger, func(context.Context) error { return nil })
	if e = httpapi.RegisterStudent(router, service); e != nil {
		t.Fatal(e)
	}
	request := func(method, path, token, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("缺少no-store")
		}
		return rec
	}
	for _, body := range []string{`{"openid":"fake"}`, `{"code":"a","client_request_id":"b","openid":"fake"}`, `{"code":"a","code":"b","client_request_id":"c"}`} {
		if rec := request("POST", "/api/v1/auth/wechat-sessions", "", "", body); rec.Code != 400 {
			t.Fatal("客户端伪造身份字段应拒绝", rec.Code)
		}
	}
	if rec := request("GET", "/api/v1/me", first.Token, "", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "SYNTHETIC_OPENID") {
		t.Fatal("本人字段投影失败", rec.Code)
	}
	if rec := request("PUT", "/api/v1/me/profile", first.Token, "http-profile", `{"nickname":"新昵称","expected_version":2}`); rec.Code != 400 {
		t.Fatal("缺布尔值须拒绝")
	}
	if rec := request("PUT", "/api/v1/me/profile", first.Token, "http-profile", `{"nickname":"新昵称","hide_identity_tags":false,"expected_version":2}`); rec.Code != 200 {
		t.Fatal("资料HTTP失败", rec.Code)
	}
	// 身份已暂存后会话写失败，也不能留下孤立账号或返回token。
	var beforeAccounts, afterAccounts int64
	if e = db.Table("public.accounts").Count(&beforeAccounts).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("CREATE FUNCTION public.fail_student_session() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic session failure'; END $$").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("CREATE TRIGGER fail_student_session BEFORE INSERT ON public.sessions FOR EACH ROW EXECUTE FUNCTION public.fail_student_session()").Error; e != nil {
		t.Fatal(e)
	}
	failedSession, failure := service.Login(ctx, "session-failure", "session-failure", "source", "student_atomic_failure")
	if e = db.Exec("DROP TRIGGER fail_student_session ON public.sessions").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("DROP FUNCTION public.fail_student_session()").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Table("public.accounts").Count(&afterAccounts).Error; e != nil {
		t.Fatal(e)
	}
	if failure != command.ErrUnavailable || failedSession.Token != "" || beforeAccounts != afterAccounts {
		t.Fatal("会话失败未回滚身份", failure)
	}
	// 受罚会话保留读取/退出，普通资料写不绕过处罚。
	if e = db.Exec("INSERT INTO public.penalties(id,account_id,level,action_kind,violation_ref,content_version,reason_ref,starts_at) VALUES (?, ?, 'severe', 'ban', ?, 1, ?, clock_timestamp())", uuid.NewString(), first.Me.ID, uuid.NewString(), uuid.NewString()).Error; e != nil {
		t.Fatal(e)
	}
	current, e := service.Current(ctx, first.Token, "student_restricted")
	if e != nil || current.Nickname != "" || len(current.AllowedActions) != 1 {
		t.Fatal("受限会话投影失败", e)
	}
	if _, e = service.UpdateProfile(ctx, first.Token, "restricted-profile", "student_restricted_write", input); e != command.ErrForbidden {
		t.Fatal("受限会话不可写资料", e)
	}
	if e = service.Logout(ctx, first.Token, "logout", "student_logout"); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Current(ctx, first.Token, "student_revoked"); e != command.ErrUnauthenticated {
		t.Fatal("退出后令牌仍可用", e)
	}
	if _, e = service.Login(ctx, "same-first", "request-same-first", "source", "student_revoked_replay"); e != command.ErrUnauthenticated {
		t.Fatal("回执不得恢复撤销会话", e)
	}
	if _, e = service.Current(ctx, second.Token, "student_other_session"); e != nil {
		t.Fatal("退出不撤销其他会话", e)
	}
	// 修改权限代次和绝对到期立即生效，不依赖Redis缓存。
	if e = db.Exec("UPDATE public.accounts SET auth_epoch=auth_epoch+1 WHERE id=?", first.Me.ID).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = service.Current(ctx, second.Token, "student_epoch"); e != command.ErrUnauthenticated {
		t.Fatal("旧代会话仍可用", e)
	}
	expired := login("expired")
	if e = db.Exec("UPDATE public.sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE account_id=?", expired.Me.ID).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = service.Current(ctx, expired.Token, "student_expired"); e != command.ErrUnauthenticated {
		t.Fatal("到期未失效", e)
	}
	// 下游密钥失败时身份/会话原子回滚，已消费code不能重复调用外部交换。
	keyService.unavailable.Store(true)
	before := exchanger.calls.Load()
	if _, e = service.Login(ctx, "key-failure", "key-failure", "source", "student_key_failure"); e != command.ErrUnavailable {
		t.Fatal("失钥必须拒绝", e)
	}
	keyService.unavailable.Store(false)
	if _, e = service.Login(ctx, "key-failure", "key-failure", "source", "student_key_repeat"); e != command.ErrUnauthenticated || exchanger.calls.Load() != before+1 {
		t.Fatal("失败code不得再次交换", e)
	}
	if _, e = service.Login(ctx, "panic-code", "panic-request", "source", "student_panic"); e != command.ErrInternal {
		t.Fatal("panic恢复失败", e)
	}
	// T008/REQ-002、018：外部故障、当前生命周期及共享限流故障均不能放行。
	exchanger.failure.Store(true)
	if _, e = service.Login(ctx, "exchange-failure", "failure-request", "source", "student_exchange_failure"); e != command.ErrUnavailable {
		t.Fatal("外部故障应503", e)
	}
	exchanger.failure.Store(false)
	deleting := login("deleting")
	if e = db.Exec("UPDATE public.accounts SET lifecycle='deleting' WHERE id=?", deleting.Me.ID).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = service.Current(ctx, deleting.Token, "student_deleting"); e != command.ErrUnauthenticated {
		t.Fatal("删除主体仍有效", e)
	}
	if _, e = service.Login(ctx, "deleting", "request-deleting", "source", "student_deleting_replay"); e != command.ErrUnauthenticated {
		t.Fatal("旧回执恢复已删除主体", e)
	}
	if _, e = service.Login(ctx, "deleting-fresh", "deleting-fresh", "source", "student_deleting_fresh"); e != command.ErrUnauthenticated {
		t.Fatal("新code不能绕过删除生命周期", e)
	}
	limitOne, _ := adminauth.NewRedisLimiter(client, time.Minute, 1)
	limitConfig := cfg
	limitConfig.SessionKey = bytes.Repeat([]byte{99}, 32)
	limited, _ := studentauth.New(restricted, logger, exchanger, store, limitOne, limitConfig)
	if _, e = limited.Login(ctx, "quota-first", "quota-first", "quota-source", "student_quota_first"); e != nil {
		t.Fatal(e)
	}
	if _, e = limited.Login(ctx, "quota-second", "quota-second", "quota-source", "student_quota_second"); e != studentauth.ErrRateLimited {
		t.Fatal("限流未生效", e)
	}
	closedClient := redis.NewClient(&redis.Options{Addr: address})
	_ = closedClient.Close()
	brokenLimiter, _ := adminauth.NewRedisLimiter(closedClient, time.Minute, 100)
	broken, _ := studentauth.New(restricted, logger, exchanger, store, brokenLimiter, cfg)
	if _, e = broken.Login(ctx, "redis-failure", "redis-failure", "source", "student_redis_failure"); e != command.ErrUnavailable {
		t.Fatal("Redis失败不得放行", e)
	}
	// 后台token不能充当学生token，窄身份也不能读取/插入后台会话。
	adminID := uuid.NewString()
	adminToken := "SYNTHETIC_ADMIN_TOKEN"
	adminHash := sha256.Sum256([]byte(adminToken))
	if e = db.Exec("INSERT INTO public.admin_accounts(id,credential_ref) VALUES (?,'synthetic-admin-ref')", adminID).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), adminID, adminHash[:]).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = service.Current(ctx, adminToken, "student_admin_token"); e != command.ErrUnauthenticated {
		t.Fatal("后台身份混用", e)
	}
	if e = restricted.Raw("SELECT count(*) FROM public.sessions WHERE subject_kind='admin'").Scan(&count).Error; e != nil || count != 0 {
		t.Fatal("身份组跨读后台会话")
	}
	if e = restricted.Exec("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), adminID, bytes.Repeat([]byte{61}, 32)).Error; e == nil {
		t.Fatal("身份组跨写后台会话")
	}
	// 数据库持久内容不含明文code、OpenID或会话令牌。
	var content string
	if e = db.Raw("SELECT coalesce(json_agg(t)::text,'') FROM public.student_login_exchanges t").Scan(&content).Error; e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{first.Token, "same-first", "SYNTHETIC_OPENID"} {
		if strings.Contains(content, secret) {
			t.Fatal("回执含明文敏感资料")
		}
	}
	for _, entry := range logs.All() {
		body, _ := json.Marshal(entry.ContextMap())
		for _, secret := range []string{first.Token, "SYNTHETIC_OPENID", "SYNTHETIC_KMS_SECRET", "SYNTHETIC_WECHAT_SECRET"} {
			if strings.Contains(string(body)+entry.Message, secret) {
				t.Fatal("敏感日志泄漏")
			}
		}
	}
	if logs.FilterMessage("学生身份操作完成").Len() == 0 {
		t.Fatal("缺完成日志")
	}
	t.Log("真实PG/Redis/HTTP与合成微信/KMS通过；未接入实际微信AppID/AppSecret或生产密钥服务")
}
