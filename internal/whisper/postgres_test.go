package whisper_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/platform/whisperrate"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

type fixtureKeys struct{ gcm cipher.AEAD }

func (k fixtureKeys) Wrap(_ context.Context, b []byte) (string, []byte, error) {
	nonce := make([]byte, k.gcm.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return "", nil, e
	}
	return "synthetic-test", append(nonce, k.gcm.Seal(nil, nonce, b, nil)...), nil
}
func (k fixtureKeys) Unwrap(_ context.Context, ref string, b []byte) ([]byte, error) {
	if ref != "synthetic-test" || len(b) < k.gcm.NonceSize() {
		return nil, errors.New("测试密钥引用无效")
	}
	return k.gcm.Open(nil, b[:k.gcm.NonceSize()], b[k.gcm.NonceSize():], nil)
}

type fixtureCatalog struct {
	mu    sync.Mutex
	value whisper.CatalogSnapshot
}

func (c *fixtureCatalog) Load(context.Context) (whisper.CatalogSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value, nil
}

type fixtureAdmission struct {
	inner    whisper.Admission
	mu       sync.Mutex
	calls    int
	released int
	failure  error
}

func (a *fixtureAdmission) Reserve(ctx context.Context, actor string, bucket string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failure != nil {
		return "", a.failure
	}
	a.calls++
	if a.inner != nil {
		return a.inner.Reserve(ctx, actor, bucket)
	}
	return uuid.NewString(), nil
}
func (a *fixtureAdmission) Release(ctx context.Context, ref string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.released++
	if a.inner != nil {
		return a.inner.Release(ctx, ref)
	}
	return nil
}

// fixtureReview 只向隔离专用路由追加工作；没有消费者批准，不冒充审核真实闭环。
type fixtureReview struct{ log *zap.Logger }

func (r fixtureReview) Append(ctx context.Context, db *gorm.DB, v whisper.ReviewSubmission) error {
	return durable.Append(ctx, db, r.log, durable.Route{Kind: "whisper_test_review", TargetKind: "moderation_task", EventType: "whisper.test_requested"}, durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: v.TaskID, AggregateVersion: v.ContentVersion})
}

// TestPostgresWhisperText 真实验证 SQL 权限、回执、并发、加密和同步投影；供应商、资料与审核入口为显式合成适配。
func TestPostgresWhisperText(t *testing.T) {
	dsn := os.Getenv("APP_WHISPER_TEXT_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离 PostgreSQL；make integration 覆盖此用例")
	}
	open := func(value string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	if !strings.Contains(dsn, needle) || parentName != "liyuanci" && parentName != "whisper_test" {
		t.Fatal("需专用隔离父库")
	}
	name := "whisper_text_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if e := parent.Exec("CREATE DATABASE " + name).Error; e != nil {
		t.Fatal("创建隔离数据库失败")
	}
	testDSN := strings.Replace(dsn, needle, "dbname="+name+" ", 1)
	admin := open(testDSN)
	ctx, log := context.Background(), zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(migration.Run(ctx, admin, log, "up"))
	must(migration.ApplyPermissions(ctx, admin, log))
	api := open(testDSN + " options='-c role=liyuanci_api'")
	actor, other := uuid.NewString(), uuid.NewString()
	token, otherToken := uuid.NewString(), uuid.NewString()
	for _, row := range [][2]string{{actor, token}, {other, otherToken}} {
		must(admin.Exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'合成主体','verified')", row[0]).Error)
		hash := sha256.Sum256([]byte(row[1]))
		must(admin.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,CURRENT_TIMESTAMP+interval '1 hour')", uuid.NewString(), row[0], hash[:]).Error)
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{9}, 32))
	gcm, _ := cipher.NewGCM(block)
	codec, _ := securedata.New(fixtureKeys{gcm}, log)
	version := int64(1)
	catalog := &fixtureCatalog{value: whisper.CatalogSnapshot{Grades: whisper.Dictionary{Version: &version, Items: []whisper.Item{{Code: "g1", Label: "合成年级"}}}}}
	admission := &fixtureAdmission{}
	if address := os.Getenv("APP_WHISPER_REDIS_TEST_ADDRESS"); address != "" {
		client := redis.NewClient(&redis.Options{Addr: address})
		t.Cleanup(func() { _ = client.Close() })
		limiter, e := whisperrate.New(client, bytes.Repeat([]byte{17}, 32), log, time.Second)
		must(e)
		admission.inner = limiter
		t.Log("文字命令准入连接真实隔离 Redis")
	}

	cfg := whisper.Config{Enabled: true, Command: command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: 24 * time.Hour, MaxAttempts: 3}, ReceiptKey: bytes.Repeat([]byte{2}, 32), CursorKey: bytes.Repeat([]byte{3}, 32)}
	service, e := whisper.NewService(api, log, codec, catalog, admission, fixtureReview{log}, cfg)
	must(e)
	anonymous := whisper.ContentInput{Body: " 原文与空白\n", Identity: whisper.IdentityInput{Mode: "anonymous"}}
	key := uuid.NewString()
	draft, e := service.CreatePost(ctx, token, key, "", anonymous)
	must(e)
	if draft.Version != 1 || draft.Status != "draft" || draft.Body == nil || *draft.Body != anonymous.Body {
		t.Fatal("创建草稿不一致", draft)
	}
	var ciphertext string
	must(admin.Raw("SELECT body_cipher::text FROM public.whisper_post_versions WHERE post_id=?", draft.ID).Scan(&ciphertext).Error)
	if strings.Contains(ciphertext, anonymous.Body) {
		t.Fatal("原稿明文落库")
	}
	var tasks int64
	must(admin.Table("public.moderation_tasks").Count(&tasks).Error)
	if tasks != 0 {
		t.Fatal("草稿产生审核任务")
	}
	_, e = service.GetPost(ctx, "", "", draft.ID, false)
	if !errors.Is(e, command.ErrNotFound) {
		t.Fatal("公开泄漏草稿", e)
	}
	_, e = service.GetPost(ctx, otherToken, "", draft.ID, true)
	if !errors.Is(e, command.ErrNotFound) {
		t.Fatal("跨主体访问", e)
	}
	sent, e := service.ChangePost(ctx, token, uuid.NewString(), "", draft.ID, "submit", whisper.PostInput{ExpectedVersion: 1})
	must(e)
	if sent.Version != 2 || sent.Status != "scanning" {
		t.Fatal("送审未推进", sent)
	}
	replay, e := service.CreatePost(ctx, token, key, "", anonymous)
	must(e)
	if replay.ID != draft.ID || replay.Version != 2 || admission.calls != 2 {
		t.Fatal("重放重复限频或返回历史状态", replay, admission.calls)
	}
	changed := anonymous
	changed.Body = "不同原文"
	_, e = service.CreatePost(ctx, token, key, "", changed)
	if !errors.Is(e, command.ErrIdempotencyConflict) {
		t.Fatal("未拒绝同键不同输入", e)
	}
	_, e = service.ChangePost(ctx, token, uuid.NewString(), "", draft.ID, "withdraw", whisper.PostInput{ExpectedVersion: 1})
	if !errors.Is(e, command.ErrVersionConflict) {
		t.Fatal("未拒绝旧资源版本", e)
	}
	withdrawn, e := service.ChangePost(ctx, token, uuid.NewString(), "", draft.ID, "withdraw", whisper.PostInput{ExpectedVersion: 2})
	must(e)
	if withdrawn.Status != "withdrawn" || withdrawn.CommentsEpoch != 2 {
		t.Fatal("撤回代次错误")
	}
	var status string
	must(admin.Raw("SELECT status FROM public.moderation_tasks LIMIT 1").Scan(&status).Error)
	if status != "cancelled" {
		t.Fatal("未取消旧审核")
	}
	// 同一版本的并发完整替换只能成功一次，正文版本与资源版本分别推进。
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := service.ChangePost(ctx, token, uuid.NewString(), "", draft.ID, "update", whisper.PostInput{ExpectedVersion: 3, ContentInput: changed})
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, command.ErrVersionConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("并发覆盖", ok, conflict)
	}
	own, e := service.GetPost(ctx, token, "", draft.ID, true)
	must(e)
	if own.(whisper.OwnerPost).ContentVersion != 2 {
		t.Fatal("正文版本未推进")
	}
	// 半匿名资料变更与隐藏设置即时降级；批准由隔离夹具显式构造，不运行真实审核。
	semi := whisper.ContentInput{Body: "已审测试文字", Identity: whisper.IdentityInput{Mode: "semi", DictionaryVersion: &version, Tags: &whisper.Tags{Campus: "wushan", Grade: "g1", Gender: "undisclosed"}}}
	semiDraft, e := service.CreatePost(ctx, otherToken, uuid.NewString(), "", semi)
	must(e)
	approve := func(id string, identity whisper.PublicIdentity) {
		t.Helper()
		raw, _ := json.Marshal(identity)
		must(admin.Transaction(func(db *gorm.DB) error {
			if e := db.Exec("UPDATE public.whisper_post_versions SET review_state='approved',public_body='已审安全文字',public_identity=?,school_related=true,notice_codes='[\"UNVERIFIED_SCHOOL\"]' WHERE post_id=?", string(raw), id).Error; e != nil {
				return e
			}
			return db.Exec("UPDATE public.whisper_posts SET review_state='approved',publish_intent=true,published_content_version=current_content_version,published_at=date_trunc('second',clock_timestamp()) WHERE id=?", id).Error
		}))
	}
	approve(semiDraft.ID, whisper.PublicIdentity{Mode: "semi", Label: "不可返回此输入标签", Tags: semi.Identity.Tags, IsAuthor: true})
	public, e := service.GetPost(ctx, "", "", semiDraft.ID, false)
	must(e)
	p := public.(whisper.PublicPost)
	alias := p.Identity.Label
	if p.Identity.Mode != "semi" || p.NoticeText == nil || *p.NoticeText != "未经证实" {
		t.Fatal("已审投影错误", p)
	}
	raw, _ := json.Marshal(p)
	if bytes.Contains(raw, []byte(other)) {
		t.Fatal("公开泄漏账号")
	}
	must(admin.Exec("UPDATE public.accounts SET hide_identity_tags=true WHERE id=?", other).Error)
	public, e = service.GetPost(ctx, "", "", semiDraft.ID, false)
	must(e)
	p = public.(whisper.PublicPost)
	if p.Identity.Mode != "anonymous" || p.Identity.Tags != nil || p.Identity.Label != alias {
		t.Fatal("隐藏设置未即时生效")
	}
	must(admin.Exec("UPDATE public.accounts SET hide_identity_tags=false WHERE id=?", other).Error)
	catalog.mu.Lock()
	catalog.value.Grades.Items = []whisper.Item{{Code: "g2", Label: "合成新年级"}}
	catalog.mu.Unlock()
	public, e = service.GetPost(ctx, "", "", semiDraft.ID, false)
	must(e)
	if public.(whisper.PublicPost).Identity.Mode != "anonymous" {
		t.Fatal("已停用标签仍公开")
	}
	changedVersion := int64(2)
	semi.Identity.DictionaryVersion = &changedVersion
	_, e = service.CreatePost(ctx, otherToken, uuid.NewString(), "", semi)
	if !errors.Is(e, whisper.ErrDictionaryChanged) {
		t.Fatal("字典未校验", e)
	}
	catalog.mu.Lock()
	catalog.value.Grades = whisper.Dictionary{}
	catalog.mu.Unlock()
	_, e = service.CreatePost(ctx, otherToken, uuid.NewString(), "", semi)
	if !errors.Is(e, whisper.ErrModeUnavailable) {
		t.Fatal("缺字典未关闭半匿名", e)
	}
	// 昵称候选经合成批准后才能被引用；新 active 不影响旧快照，撤销旧版本即时隐藏。
	nick, e := service.SetNickname(ctx, token, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: 1, Label: "合成昵称"})
	must(e)
	if nick.Active != nil || nick.Candidate == nil || nick.Version != 2 {
		t.Fatal("候选覆盖 active", nick)
	}
	must(admin.Exec("UPDATE public.whisper_nickname_versions SET review_state='approved',public_label='合成昵称' WHERE account_id=? AND nickname_version=1", actor).Error)
	must(admin.Exec("UPDATE public.whisper_preferences SET active_nickname_version=1 WHERE account_id=?", actor).Error)
	named := whisper.ContentInput{Body: "固定昵称测试", Identity: whisper.IdentityInput{Mode: "named", NicknameVersion: &version}}
	namedPost, e := service.CreatePost(ctx, token, uuid.NewString(), "", named)
	must(e)
	approve(namedPost.ID, whisper.PublicIdentity{Mode: "named", Label: "合成昵称", IsAuthor: true})
	_, e = service.SetNickname(ctx, token, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: 2, Label: "合成新昵称"})
	must(e)
	must(admin.Exec("UPDATE public.whisper_nickname_versions SET review_state='approved',public_label='合成新昵称' WHERE account_id=? AND nickname_version=2", actor).Error)
	must(admin.Exec("UPDATE public.whisper_preferences SET active_nickname_version=2 WHERE account_id=?", actor).Error)
	public, e = service.GetPost(ctx, "", "", namedPost.ID, false)
	must(e)
	if public.(whisper.PublicPost).Identity.Label != "合成昵称" {
		t.Fatal("历史快照被新昵称替换")
	}
	must(admin.Exec("UPDATE public.whisper_nickname_versions SET revoked_at=clock_timestamp() WHERE account_id=? AND nickname_version=1", actor).Error)
	_, e = service.GetPost(ctx, "", "", namedPost.ID, false)
	if !errors.Is(e, command.ErrNotFound) {
		t.Fatal("撤销后仍公开", e)
	}
	// 分页绑定主体及入口；过滤项仍推进候选游标，防止页面原地循环。
	page, e := service.ListPosts(ctx, token, "", true, whisper.Query{Limit: 1})
	must(e)
	next := page.(whisper.Page[whisper.OwnerPost]).NextCursor
	if next == nil {
		t.Fatal("缺本人分页游标")
	}
	_, e = service.ListPosts(ctx, otherToken, "", true, whisper.Query{Limit: 1, Cursor: *next})
	if !errors.Is(e, command.ErrInvalid) {
		t.Fatal("游标跨主体复用", e)
	}
	_, e = service.ListPosts(ctx, token, "", false, whisper.Query{Limit: 1, Cursor: *next})
	if !errors.Is(e, command.ErrInvalid) {
		t.Fatal("游标跨入口复用", e)
	}
	// 同步隐私屏障遮蔽所有公开读取与本人原稿，幂等回放也不能回放旧正文。
	must(admin.Exec("INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version) VALUES (?,'whisper_post',?,1,3)", uuid.NewString(), draft.ID).Error)
	replay, e = service.CreatePost(ctx, token, key, "", anonymous)
	must(e)
	if replay.Status != "privacy_hold" || replay.Body != nil || replay.IdentityInput != nil {
		t.Fatal("回执绕过当前屏障", replay)
	}
	noReview, e := whisper.NewService(api, log, codec, catalog, admission, nil, cfg)
	must(e)
	_, e = noReview.SetNickname(ctx, otherToken, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: 1, Label: "无法送审"})
	if !errors.Is(e, command.ErrUnavailable) {
		t.Fatal("缺审核适配仍接受", e)
	}
	// 真实 HTTP 传输与领域、SQL 贯通；可选输出供固定 OpenAPI schema 逐项验证。
	router := httpapi.New(log, func(context.Context) error { return nil })
	must(httpapi.RegisterWhispers(router, service))
	samples := []map[string]any{}
	request := func(method, path, body, schema string, status int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+otherToken)
		if method != "GET" {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", uuid.NewString())
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != status || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
			t.Fatal("HTTP 协议错误", method, path, response.Code, response.Body.String())
		}
		var envelope map[string]any
		must(json.Unmarshal(response.Body.Bytes(), &envelope))
		samples = append(samples, map[string]any{"schema": schema, "response": envelope, "method": method, "path": path, "status": status})
		return envelope["data"].(map[string]any)
	}
	request("GET", "/api/v1/whispers/config", "", "ModuleConfigResponse", 200)
	request("GET", "/api/v1/whispers/help", "", "HelpDirectoryResponse", 200)
	request("GET", "/api/v1/me/whispers", "", "WorkspaceResponse", 200)
	inputBody := `{"body":"HTTP 合成草稿","identity":{"mode":"anonymous"}}`
	created := request("POST", "/api/v1/me/whispers/posts", inputBody, "OwnerPostResponse", 201)
	wireID := created["id"].(string)
	request("GET", "/api/v1/me/whispers/posts/"+wireID, "", "OwnerPostResponse", 200)
	request("GET", "/api/v1/me/whispers/posts?limit=1", "", "OwnerPostPageResponse", 200)
	request("PUT", "/api/v1/me/whispers/posts/"+wireID, `{"expected_version":1,"body":"HTTP 合成新稿","identity":{"mode":"anonymous"}}`, "OwnerPostResponse", 200)
	request("POST", "/api/v1/me/whispers/posts/"+wireID+"/submit", `{"expected_version":2}`, "OwnerPostResponse", 202)
	request("POST", "/api/v1/me/whispers/posts/"+wireID+"/withdraw", `{"expected_version":3}`, "OwnerPostResponse", 200)
	request("GET", "/api/v1/me/whispers/nickname", "", "NicknameStateResponse", 200)
	request("PUT", "/api/v1/me/whispers/nickname", `{"expected_version":1,"label":"HTTP 合成昵称"}`, "NicknameStateResponse", 202)
	request("GET", "/api/v1/whispers/posts", "", "PublicPostPageResponse", 200)
	visible := request("GET", "/api/v1/whispers/posts/"+semiDraft.ID, "", "PublicPostResponse", 200)
	if visible["my_hug"] == nil {
		t.Fatal("登录未抱抱须有初始目标态")
	}
	if output := os.Getenv("APP_WHISPER_TEXT_WIRE_OUTPUT"); output != "" {
		b, e := json.MarshalIndent(samples, "", "  ")
		must(e)
		must(os.WriteFile(output, b, 0600))
	}
	// 技术回执期限过后仍只返回当前状态；失效结果不能使用旧键重新创建。
	retainedKey := uuid.NewString()
	retained, e := service.CreatePost(ctx, otherToken, retainedKey, "", anonymous)
	must(e)
	hash := sha256.Sum256([]byte(retainedKey))
	must(admin.Exec("UPDATE public.command_receipts SET expires_at=clock_timestamp()-interval '1 day' WHERE actor_id=? AND key_hash=?", other, hash[:]).Error)
	calls := admission.calls
	again, e := service.CreatePost(ctx, otherToken, retainedKey, "", anonymous)
	must(e)
	if again.ID != retained.ID || admission.calls != calls {
		t.Fatal("过期键重新执行")
	}
	must(admin.Exec("UPDATE public.command_receipts SET result_ref=? WHERE actor_id=? AND key_hash=?", uuid.NewString(), other, hash[:]).Error)
	_, e = service.CreatePost(ctx, otherToken, retainedKey, "", anonymous)
	if !errors.Is(e, command.ErrNotFound) || admission.calls != calls {
		t.Fatal("失效结果重新创建", e)
	}
	must(admin.Exec("UPDATE public.accounts SET verification='unverified' WHERE id=?", other).Error)
	_, e = service.CreatePost(ctx, otherToken, uuid.NewString(), "", anonymous)
	if !errors.Is(e, whisper.ErrVerificationRequired) {
		t.Fatal("未认证仍可发帖", e)
	}
	_, e = service.SetNickname(ctx, otherToken, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: 2, Label: "未认证合成昵称"})
	must(e)
	must(admin.Exec("UPDATE public.accounts SET verification='verified' WHERE id=?", other).Error)
	admission.failure = &whisper.RateError{Seconds: 7}
	_, e = service.CreatePost(ctx, otherToken, uuid.NewString(), "", anonymous)
	var rate *whisper.RateError
	if !errors.As(e, &rate) || rate.Seconds != 7 {
		t.Fatal("限频错误丢失", e)
	}
	cfg.Enabled = false
	disabled, e := whisper.NewService(api, log, codec, catalog, admission, nil, cfg)
	must(e)
	_, e = disabled.GetPost(ctx, "", "", semiDraft.ID, false)
	if !errors.Is(e, whisper.ErrModuleUnavailable) {
		t.Fatal("开关未关闭")
	}
	help, e := disabled.Help(ctx, "")
	must(e)
	if help.Status != "unavailable" || help.Items == nil {
		t.Fatal("缺援助资料泄漏示例")
	}
	if e = migration.Run(ctx, admin, log, "down"); e == nil || !strings.Contains(e.Error(), "生命周期回执") {
		t.Fatal("生命周期回执未阻止回退", e)
	}
	var retainedCount int64
	must(admin.Table("public.whisper_posts").Count(&retainedCount).Error)
	if retainedCount == 0 {
		t.Fatal("拒绝回退仍丢失业务数据")
	}
	t.Log("真实PostgreSQL与API运行角色验证通过；审核批准、资料和KMS为隔离合成夹具，未启用模块")
}
