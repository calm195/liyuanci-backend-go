package verification_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/identity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/verification"
)

type keys struct{}

func (keys) Wrap(_ context.Context, data []byte) (string, []byte, error) {
	block, _ := aes.NewCipher(bytes.Repeat([]byte{38}, 32))
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	if _, e := rand.Read(nonce); e != nil {
		return "", nil, e
	}
	return "synthetic-card-wrap", append(nonce, gcm.Seal(nil, nonce, data, nil)...), nil
}
func (keys) Unwrap(_ context.Context, ref string, data []byte) ([]byte, error) {
	if ref != "synthetic-card-wrap" || len(data) < 28 {
		return nil, errors.New("SYNTHETIC_KMS_SECRET")
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{38}, 32))
	gcm, _ := cipher.NewGCM(block)
	return gcm.Open(nil, data[:12], data[12:], nil)
}
func (keys) Digest(_ context.Context, ref, purpose string, data []byte) ([]byte, error) {
	return securedata.LookupDigest(bytes.Repeat([]byte{81}, 32), purpose, string(data))
}

type exchange struct{}

func (exchange) Exchange(context.Context, string) ([]byte, error) {
	return nil, errors.New("本测试不执行供应商交换")
}

type limiter struct{}

func (limiter) Allow(context.Context, string) (bool, error) { return true, nil }

type rules struct{}

func (rules) Validate(_ context.Context, number, name string) error {
	if !strings.HasPrefix(number, "SYNTHETIC_") || !strings.HasPrefix(name, "SYNTHETIC_") {
		return command.ErrInvalid
	}
	return nil
}

type objects struct {
	data   []byte
	hook   func()
	failed bool
}

func (o *objects) Open(_ context.Context, key, version string) (io.ReadCloser, error) {
	if o.hook != nil {
		o.hook()
	}
	if o.failed || key != "SYNTHETIC_PRIVATE_KEY" || version != "synthetic-version" {
		return nil, errors.New("SYNTHETIC_PROVIDER_SECRET")
	}
	return io.NopCloser(bytes.NewReader(o.data)), nil
}

// T011/REQ-002、015、018、020：真实隔离PostgreSQL与HTTP，身份/密钥/对象/学校规则使用合成材料，不执行跨端联调。
func TestPostgresVerification(t *testing.T) {
	dsn := os.Getenv("APP_VERIFICATION_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PostgreSQL：按模块要求提供隔离依赖后运行相关集成测试")
	}
	ctx := context.Background()
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)
	open := func(role string) *gorm.DB {
		t.Helper()
		suffix := ""
		if role != "" {
			suffix = " options='-c role=" + role + "'"
		}
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn + suffix, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if e != nil {
			t.Fatal("数据库连接失败")
		}
		pool, _ := db.DB()
		t.Cleanup(func() { pool.Close() })
		return db
	}
	root := open("")
	if e := migration.Run(ctx, root, log, "up"); e != nil {
		t.Fatal(e)
	}
	if e := migration.ApplyPermissions(ctx, root, log); e != nil {
		t.Fatal(e)
	}
	if e := migration.ApplyPermissions(ctx, root, log); e != nil {
		t.Fatal(e)
	}
	studentDB, adminDB := open("liyuanci_identity_maintenance"), open("liyuanci_admin_api")
	codec, _ := securedata.New(keys{}, log)
	store, _ := identity.New(studentDB, codec, keys{}, log)
	if e := store.Configure(ctx, "student_number", "synthetic-number"); e != nil {
		t.Fatal(e)
	}
	if e := store.BeginRotation(ctx, "student_number", "synthetic-number-next"); e != nil {
		t.Fatal(e)
	}
	adminStore, _ := identity.New(adminDB, codec, keys{}, log)
	sessions, e := studentauth.New(studentDB, log, exchange{}, store, limiter{}, studentauth.Config{VerificationRequests: true, Command: command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}, SessionTTL: time.Hour, ExchangeTTL: time.Minute, SessionKey: bytes.Repeat([]byte{35}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	bucket := &objects{}
	cfg := verification.Config{Command: command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}, ReceiptKey: bytes.Repeat([]byte{92}, 32), MaxCardBytes: 4096}
	student, e := verification.New(studentDB, log, codec, store, rules{}, bucket, cfg)
	if e != nil {
		t.Fatal(e)
	}
	admin, e := verification.New(adminDB, log, codec, adminStore, rules{}, bucket, cfg)
	if e != nil {
		t.Fatal(e)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if e := root.Exec(sql, args...).Error; e != nil {
			t.Fatal(e)
		}
	}
	createStudent := func() (string, string) {
		t.Helper()
		id, token := uuid.NewString(), uuid.NewString()
		if e := root.Create(&entity.Account{ID: id, Nickname: "合成用户"}).Error; e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256([]byte(token))
		if e := root.Create(&entity.Session{ID: uuid.NewString(), AccountID: &id, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error; e != nil {
			t.Fatal(e)
		}
		return id, token
	}
	actor, adminToken := uuid.NewString(), uuid.NewString()
	credential := "synthetic-admin"
	if e := root.Create(&entity.AdminAccount{ID: actor, CredentialRef: &credential}).Error; e != nil {
		t.Fatal(e)
	}
	exec("INSERT INTO public.admin_role_bindings(admin_id,role) VALUES (?,?)", actor, "review_operator")
	hash := sha256.Sum256([]byte(adminToken))
	if e := root.Create(&entity.Session{ID: uuid.NewString(), AdminID: &actor, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error; e != nil {
		t.Fatal(e)
	}
	account, token := createStudent()
	initialMe, e := sessions.Current(ctx, token, "review_me")
	if e != nil || !slices.Contains(initialMe.AllowedActions, "postMeVerificationRequests") {
		t.Fatal("未认证账号缺已装配动作", e)
	}
	other, otherToken := createStudent()
	// 合成私有图片与对象键均用真实信封算法；扫描是已通过状态夹具，不冒充真实供应商。
	image := []byte("SYNTHETIC_PNG_BYTES")

	makeAsset := func(owner string) string {
		t.Helper()
		id := uuid.NewString()
		keyEnv, e := codec.Encrypt(ctx, securedata.Binding{Purpose: "student_card_key", ObjectID: id, Version: 1}, []byte("SYNTHETIC_PRIVATE_KEY"))
		if e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(keyEnv)
		if e = root.Create(&entity.Asset{ID: id, OwnerID: owner, Purpose: "student_card", EncryptedObjectKey: b, ObjectVersion: "synthetic-version", WrappedKeyRef: "synthetic-card-wrap", Status: "active", ScanStatus: "approved"}).Error; e != nil {
			t.Fatal(e)
		}
		return id
	}
	card := makeAsset(account)
	otherCard := makeAsset(other)
	imageEnv, _ := codec.Encrypt(ctx, securedata.Binding{Purpose: "student_card", ObjectID: card, Version: 1}, image)
	bucket.data, _ = json.Marshal(imageEnv)
	input := verification.Input{Name: "SYNTHETIC_NAME", StudentNumber: "SYNTHETIC_NUMBER", AssetID: card}
	if _, e = student.Submit(ctx, otherToken, "cross-owner", "review_test", input); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("跨账号图片未拒绝", e)
	}
	exec("UPDATE public.assets SET scan_status='pending' WHERE id=?", card)
	if _, e = student.Submit(ctx, token, "pending-card", "review_test", input); !errors.Is(e, verification.ErrAssetNotReady) {
		t.Fatal("待扫描图片未拒绝", e)
	}
	exec("UPDATE public.assets SET scan_status='approved' WHERE id=?", card)
	result, e := student.Submit(ctx, token, "submit", "review_test", input)
	if e != nil || result.Status != "pending" || result.Version != 1 {
		t.Fatal("申请失败", e, result)
	}
	pendingMe, e := sessions.Current(ctx, token, "review_me")
	if e != nil || pendingMe.Verification != "pending" || slices.Contains(pendingMe.AllowedActions, "postMeVerificationRequests") {
		t.Fatal("待审核动作错误", e)
	}
	replay, e := student.Submit(ctx, token, "submit", "review_test", input)
	if e != nil || replay.RequestID != result.RequestID {
		t.Fatal("申请重放失败", e)
	}
	changed := input
	changed.Name = "SYNTHETIC_CHANGED"
	if _, e = student.Submit(ctx, token, "submit", "review_test", changed); !errors.Is(e, command.ErrIdempotencyConflict) {
		t.Fatal("幂等正文冲突未拒绝", e)
	}
	if _, e = student.Current(ctx, otherToken, "review_test"); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("泄露他人申请", e)
	}
	page, e := admin.List(ctx, adminToken, "review_test", "", 1)
	if e != nil || len(page.Items) != 1 {
		t.Fatal("队列失败", e)
	}
	encoded, _ := json.Marshal(page)
	if bytes.Contains(encoded, []byte("SYNTHETIC_")) || bytes.Contains(encoded, []byte(card)) {
		t.Fatal("列表泄露材料")
	}
	detail, e := admin.Detail(ctx, adminToken, "review_test", result.RequestID)
	if e != nil || detail.Name != input.Name || detail.StudentNumber != input.StudentNumber {
		t.Fatal("详情失败", e)
	}
	data, e := admin.Card(ctx, adminToken, "review_test", result.RequestID)
	if e != nil || !bytes.Equal(data, image) {
		t.Fatal("原卡读取失败", e)
	}
	clear(data)
	rejected, e := admin.Decide(ctx, adminToken, "reject", "review_test", result.RequestID, verification.Decision{Decision: "rejected", ExpectedVersion: 1, Reason: "材料不清晰", RevisionSuggestion: "请重传清晰学生卡"})
	if e != nil || rejected.Version != 2 || rejected.Status != "rejected" {
		t.Fatal("驳回失败", e)
	}
	if _, e = student.Submit(ctx, token, "resubmit-missing", "review_test", input); !errors.Is(e, command.ErrInvalid) {
		t.Fatal("重提缺版本未拒绝", e)
	}
	stale := int64(1)
	input.ExpectedVersion = &stale
	if _, e = student.Submit(ctx, token, "resubmit-stale", "review_test", input); !errors.Is(e, command.ErrVersionConflict) {
		t.Fatal("旧版本重提未拒绝", e)
	}
	version := rejected.Version
	input.StudentNumber = "SYNTHETIC_CORRECTED_NUMBER"
	input.ExpectedVersion = &version
	resubmitted, e := student.Submit(ctx, token, "resubmit", "review_test", input)
	if e != nil || resubmitted.Version != 3 || resubmitted.RequestID != result.RequestID || resubmitted.Reason != nil {
		t.Fatal("重提失败", e, resubmitted)
	}
	if _, e = admin.Decide(ctx, adminToken, "old-decision", "review_test", result.RequestID, verification.Decision{Decision: "approved", ExpectedVersion: 1}); !errors.Is(e, command.ErrVersionConflict) {
		t.Fatal("旧审核覆盖新材料", e)
	}
	// 两个不同幂等键同时审核，只能一个事务生效，另一个应得到版本冲突。
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, conflict := 0, 0
	for _, key := range []string{"approve-a", "approve-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, e := admin.Decide(ctx, adminToken, key, "review_parallel", result.RequestID, verification.Decision{Decision: "approved", ExpectedVersion: 3})
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				success++
			} else if errors.Is(e, command.ErrVersionConflict) {
				conflict++
			} else {
				t.Error("并发审核失败", e)
			}
		}(key)
	}
	wg.Wait()
	if success != 1 || conflict != 1 {
		t.Fatal("并发审核非单次生效", success, conflict)
	}
	current, e := student.Current(ctx, token, "review_test")
	if e != nil || current.Status != "approved" || current.Version != 4 {
		t.Fatal("当前结果错误", e)
	}
	var saved entity.Account
	root.Where("id=?", account).Take(&saved)
	me, e := sessions.Current(ctx, token, "review_me")
	if e != nil || me.Verification != "verified" || slices.Contains(me.AllowedActions, "postMeVerificationRequests") {
		t.Fatal("通过后Me未即时更新", e)
	}
	if saved.Verification != "verified" {
		t.Fatal("资格未更新")
	}
	var count int64
	root.Model(&entity.IdentityLookupClaim{}).Where("account_id=? AND purpose='student_number'", account).Count(&count)
	if count != 2 {
		t.Fatal("双代学号唯一占用缺失")
	}
	if e := store.FinishRotation(ctx, "student_number"); e != nil {
		t.Fatal("审核绑定后轮换无法完成", e)
	}

	secondInput := verification.Input{Name: "SYNTHETIC_OTHER", StudentNumber: input.StudentNumber, AssetID: otherCard}
	if _, e = student.Submit(ctx, otherToken, "duplicate-registration", "review_test", secondInput); !errors.Is(e, command.ErrConflict) {
		t.Fatal("注册重复学号未拒绝", e)
	}
	// 驳回修正已原子释放原学号，另一个账号可重新绑定该原值。
	secondInput.StudentNumber = "SYNTHETIC_NUMBER"
	second, e := student.Submit(ctx, otherToken, "other-submit", "review_test", secondInput)
	if e != nil {
		t.Fatal("旧学号未释放", e)
	}
	var secondRow entity.IdentityReview
	root.Where("id=?", second.RequestID).Take(&secondRow)
	// 即使材料被错误的后台存储操作改成另一账号学号，通过前仍必须核验注册绑定。
	altered, _ := json.Marshal(map[string]string{"name": secondInput.Name, "student_number": input.StudentNumber})
	alteredEnv, _ := codec.Encrypt(ctx, securedata.Binding{Purpose: "identity_review", ObjectID: second.RequestID, Version: 1}, altered)
	encodedAltered, _ := json.Marshal(alteredEnv)
	exec("UPDATE public.identity_reviews SET data_cipher=? WHERE id=?", json.RawMessage(encodedAltered), second.RequestID)
	if _, e = admin.Decide(ctx, adminToken, "duplicate-number", "review_test", second.RequestID, verification.Decision{Decision: "approved", ExpectedVersion: 1}); !errors.Is(e, command.ErrConflict) {
		t.Fatal("审核未复核注册学号", e)
	}
	unchanged, _ := student.Current(ctx, otherToken, "review_test")
	if unchanged.Version != 1 || unchanged.Status != "pending" {
		t.Fatal("冲突未回滚")
	}
	exec("UPDATE public.identity_reviews SET data_cipher=? WHERE id=?", secondRow.DataCipher, second.RequestID)

	// 审计失败必须回滚审核及敏感读取，不能返回明文或半个认证状态。
	exec("REVOKE INSERT ON public.access_audits FROM liyuanci_admin_api")
	if out, e := admin.Detail(ctx, adminToken, "review_test", result.RequestID); e == nil || out.Name != "" {
		t.Fatal("审计失败泄露明文")
	}
	rollbackInput := verification.Decision{Decision: "rejected", ExpectedVersion: 1, Reason: "合成驳回", RevisionSuggestion: "合成建议"}
	if _, e = admin.Decide(ctx, adminToken, "audit-rollback", "review_test", second.RequestID, rollbackInput); e == nil {
		t.Fatal("审计失败仍然提交决定")
	}
	unchanged, _ = student.Current(ctx, otherToken, "review_test")
	if unchanged.Version != 1 || unchanged.Status != "pending" {
		t.Fatal("审计失败留下部分审核结果")
	}
	exec("GRANT INSERT ON public.access_audits TO liyuanci_admin_api")
	exec("DELETE FROM public.admin_role_bindings WHERE admin_id=?", actor)
	if _, e = admin.Detail(ctx, adminToken, "review_test", result.RequestID); !errors.Is(e, command.ErrForbidden) {
		t.Fatal("撤权未生效", e)
	}
	if _, e = admin.Decide(ctx, adminToken, "approve-a", "review_test", result.RequestID, verification.Decision{Decision: "approved", ExpectedVersion: 3}); !errors.Is(e, command.ErrForbidden) {
		t.Fatal("撤权后回执仍放行", e)
	}
	exec("INSERT INTO public.admin_role_bindings(admin_id,role) VALUES (?,?)", actor, "review_operator")
	bucket.hook = func() { exec("UPDATE public.accounts SET lifecycle='deleting' WHERE id=?", account) }
	if data, e = admin.Card(ctx, adminToken, "review_test", result.RequestID); e == nil || data != nil {
		t.Fatal("外部读取期间注销仍返回图片", e)
	}
	bucket.hook = nil
	exec("UPDATE public.accounts SET lifecycle='active' WHERE id=?", account)
	// 本端HTTP验证严格字段、认证、状态码、no-store和二进制代理，不涉及其他端。
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if e = httpapi.RegisterVerification(router, student, admin); e != nil {
		t.Fatal(e)
	}
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "http-test")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := request("GET", "/api/v1/me/verification-request", token, ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("本人HTTP状态", w.Code)
	}
	if w := request("POST", "/api/v1/me/verification-requests", token, `{"unknown":true}`); w.Code != 400 {
		t.Fatal("未知字段未拒绝", w.Code)
	}
	if w := request("POST", "/api/v1/me/verification-requests", token, `{"student_number":"SYNTHETIC_NUMBER","name":"SYNTHETIC_NAME","asset_id":"`+card+`","expected_version":null}`); w.Code != 400 {
		t.Fatal("显式null版本未拒绝", w.Code)
	}
	if w := request("GET", "/api/v1/admin/identity-reviews/"+result.RequestID+"/student-card", adminToken, ""); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), image) {
		t.Fatal("原卡HTTP错误", w.Code)
	}
	if w := request("GET", "/api/v1/admin/identity-reviews?limit=0", adminToken, ""); w.Code != 400 {
		t.Fatal("页上限未校验", w.Code)
	}
	// T011/REQ-002：不同账号同时注册相同学号，只能一个申请及唯一占用提交。
	left, leftToken := createStudent()
	right, rightToken := createStudent()
	leftCard, rightCard := makeAsset(left), makeAsset(right)
	successes, conflicts := 0, 0
	for _, item := range []struct{ token, card, key string }{{leftToken, leftCard, "reserve-left"}, {rightToken, rightCard, "reserve-right"}} {
		wg.Add(1)
		go func(token, card, key string) {
			defer wg.Done()
			_, e := student.Submit(ctx, token, key, "review_registration", verification.Input{Name: "SYNTHETIC_SAME_NAME", StudentNumber: "SYNTHETIC_CONCURRENT_NUMBER", AssetID: card})
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				successes++
			} else if errors.Is(e, command.ErrConflict) {
				conflicts++
			} else {
				t.Error("并发注册失败", e)
			}
		}(item.token, item.card, item.key)
	}
	wg.Wait()
	if successes != 1 || conflicts != 1 {
		t.Fatal("相同学号注册未串行化", successes, conflicts)
	}
	// T011：队列超过一页时返回签名游标；篡改或跨主体游标必须400。
	third, thirdToken := createStudent()
	thirdCard := makeAsset(third)
	thirdInput := verification.Input{Name: "SYNTHETIC_THIRD", StudentNumber: "SYNTHETIC_THIRD_NUMBER", AssetID: thirdCard}
	if _, e = student.Submit(ctx, thirdToken, "third-submit", "review_test", thirdInput); e != nil {
		t.Fatal(e)
	}
	queue, e := admin.List(ctx, adminToken, "review_test", "", 1)
	if e != nil || queue.NextCursor == nil {
		t.Fatal("分页游标缺失", e)
	}
	nextPage, e := admin.List(ctx, adminToken, "review_test", *queue.NextCursor, 1)
	if e != nil || len(nextPage.Items) != 1 || nextPage.Items[0].ID == queue.Items[0].ID {
		t.Fatal("分页重复或漏失", e)
	}
	if _, e = admin.List(ctx, adminToken, "review_test", *queue.NextCursor+"x", 1); !errors.Is(e, command.ErrInvalid) {
		t.Fatal("篡改游标未拒绝", e)
	}
	root.Model(&entity.AccessAudit{}).Where("actor_id=?", actor).Count(&count)
	if count < 4 {
		t.Fatal("访问留迹缺失")
	}
	var rows []entity.IdentityReview
	root.Find(&rows)
	for _, r := range rows {
		if bytes.Contains(r.DataCipher, []byte("SYNTHETIC_NAME")) || bytes.Contains(r.DataCipher, []byte("SYNTHETIC_NUMBER")) {
			t.Fatal("材料明文落库")
		}
	}
	for _, entry := range logs.All() {
		b, _ := json.Marshal(entry.ContextMap())
		if strings.Contains(entry.Message, "SYNTHETIC_") || bytes.Contains(b, []byte("SYNTHETIC_")) || bytes.Contains(b, []byte(token)) || bytes.Contains(b, []byte(adminToken)) {
			t.Fatal("日志泄露")
		}
	}
}
