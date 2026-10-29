package whisperapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// TestPostgresLocalBusiness 验证真实PG/Redis下单登录身份、组合API、人工树洞消费、岗位隔离及文件日志恢复。
// 学生身份为隔离fixture，不替代真实wx.login；无外部审核、通知或清理供应商。
func TestPostgresLocalBusiness(t *testing.T) {
	dsn, redisAddress := os.Getenv("APP_WHISPER_TEXT_TEST_DSN"), os.Getenv("APP_WHISPER_REDIS_TEST_ADDRESS")
	if dsn == "" || redisAddress == "" {
		t.Skip("需显式隔离PG/Redis，make integration覆盖")
	}
	ctx, log := context.Background(), zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal("本地业务验证失败", e)
		}
	}
	open := func(value string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		must(e)
		p, e := db.DB()
		must(e)
		t.Cleanup(func() { _ = p.Close() })
		return db
	}
	parent := open(dsn)
	var base string
	must(parent.Raw("SELECT current_database()").Scan(&base).Error)
	if base != "liyuanci" && base != "whisper_test" {
		t.Fatal("仅允许隔离测试父库")
	}
	name := "local_business_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(parent.Exec("CREATE DATABASE " + name).Error)
	businessDSN := strings.Replace(dsn, "dbname="+base+" ", "dbname="+name+" ", 1)
	if businessDSN == dsn {
		t.Fatal("未定位隔离库")
	}
	root := open(businessDSN)
	must(migration.Run(ctx, root, log, "up"))
	must(migration.ApplyPermissions(ctx, root, log))
	must(blockpolicy.Enable(ctx, root, log))
	login := "local_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := uuid.NewString()
	must(parent.Exec("CREATE ROLE " + login + " LOGIN PASSWORD '" + password + "' IN ROLE liyuanci_api,liyuanci_identity_maintenance,liyuanci_admin_api,liyuanci_admin_maintenance,liyuanci_domain_worker,liyuanci_crisis_api,liyuanci_privacy_maintenance,liyuanci_evidence_delete_api,liyuanci_evidence_purger").Error)
	parts := strings.Fields(businessDSN)
	for i, v := range parts {
		if strings.HasPrefix(v, "user=") {
			parts[i] = "user=" + login
		}
		if strings.HasPrefix(v, "password=") {
			parts[i] = "password=" + password
		}
	}
	shared := strings.Join(parts, " ")
	for _, kind := range []string{"crisis", "school", "ordinary_violation"} {
		id := uuid.NewString()
		must(root.Create(&entity.WordlistVersion{ID: id, Type: kind, Version: 2, Status: "published"}).Error)
		must(root.Exec("UPDATE wordlist_heads SET effective_id=?,version=3 WHERE kind=?", id, kind).Error)
	}
	dir := t.TempDir()
	must(os.Chmod(dir, 0700))
	objects := filepath.Join(dir, "objects")
	must(os.Mkdir(objects, 0700))
	dc := devapp.Settings{LocalTest: true, DatabaseDSN: shared, JournalFile: filepath.Join(dir, "journal.json"), AdminPassword: "isolated-local-password", ObjectDirectory: objects}
	for i, k := range []*[]byte{&dc.WrapKey, &dc.LookupKey, &dc.SessionKey, &dc.CardKey, &dc.VerificationKey, &dc.WishKey, &dc.AdminKey, &dc.ModerationKey, &dc.RecoveryWrapKey} {
		*k = bytes.Repeat([]byte{byte(20 + i)}, 32)
	}
	devPath := filepath.Join(dir, "business.json")
	must(os.WriteFile(devPath, mustJSON(t, dc), 0600))
	dc, e := devapp.Load(devPath)
	must(e)
	must(devapp.Initialize(ctx, dc, log, "local-root"))
	cfg := testSettings()
	cfg.LocalTest = true
	cfg.DatabaseDSN = shared
	cfg.JournalFile = dc.JournalFile
	cfg.WrapKey = dc.WrapKey
	cfg.RecoveryWrapKey = dc.RecoveryWrapKey
	cfg.PrivacyKey = privacy.DeriveKey(dc.WishKey)
	cfg.SafetyKey = safety.DeriveKey(dc.WishKey)
	cfg.CatalogFile = ""
	cfg.DatabasePolling = true
	cfg.ReviewMQFile = ""
	cfg.UnlockMQFile = ""
	path := filepath.Join(dir, "whispers.json")
	must(os.WriteFile(path, mustJSON(t, cfg), 0600))
	cfg, e = config.LoadWhispers(ctx, path, "integration")
	must(e)
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	defer client.Close()
	router, closeApp, e := devapp.Open(ctx, "integration", dc, client, log, func(context.Context) error { return nil })
	must(e)
	defer closeApp()
	app, e := OpenAPI(ctx, cfg, client, log)
	must(e)
	defer app.Close()
	must(app.Register(router))
	request := func(method, path, token string, in, out any, status int) {
		t.Helper()
		raw := []byte{}
		if in != nil {
			raw = mustJSON(t, in)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s got=%d expected=%d body=%s", method, path, w.Code, status, w.Body.String())
		}
		if out != nil {
			var envelope struct{ Data json.RawMessage }
			must(json.Unmarshal(w.Body.Bytes(), &envelope))
			must(json.Unmarshal(envelope.Data, out))
		}
	}
	var admin struct{ Token string }
	request("POST", "/admin/sessions", "", map[string]string{"login_name": "local-root", "password": dc.AdminPassword}, &admin, 200)
	for _, a := range []struct {
		name, role string
		want       int
	}{{"review", "review_operator", 403}, {"crisis", "crisis_operator", 200}} {
		request("POST", "/admin/accounts", admin.Token, map[string]any{"login_name": a.name, "roles": []string{a.role}, "reason": "隔离测试岗位"}, nil, 201)
		var session struct{ Token string }
		request("POST", "/admin/sessions", "", map[string]string{"login_name": a.name, "password": dc.AdminPassword}, &session, 200)
		request("GET", "/admin/crisis-cases", session.Token, nil, nil, a.want)
	}
	actor, token := uuid.NewString(), uuid.NewString()
	must(root.Create(&entity.Account{ID: actor, Nickname: "本地合成账号", Verification: "verified"}).Error)
	hash := sha256.Sum256([]byte(token))
	must(root.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), actor, hash[:]).Error)
	request("GET", "/me", token, nil, nil, 200)
	request("GET", "/admin/wordlists", token, nil, nil, 401)
	var post whisper.OwnerPost
	request("POST", "/me/whispers/posts", token, whisper.ContentInput{Body: "本地原文人工审核", Identity: whisper.IdentityInput{Mode: "anonymous"}}, &post, 201)
	request("POST", "/me/whispers/posts/"+post.ID+"/submit", token, map[string]int64{"expected_version": post.Version}, &post, 202)
	workCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- RunContent(workCtx, path, "integration", log) }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		cancel()
		select {
		case e := <-done:
			must(e)
		case <-time.After(10 * time.Second):
			t.Error("本地worker未退出")
		}
	}()
	wait := func(fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if fn() {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("本地任务超时")
	}
	approve := func(kind, id string) {
		t.Helper()
		var task entity.ModerationTask
		wait(func() bool {
			return root.Table("moderation_tasks t").Select("t.*").Joins("JOIN content_registry c ON c.id=t.content_id").Where("c.kind=? AND c.entity_id=? AND t.status='human_required'", kind, id).Take(&task).Error == nil
		})
		request("GET", "/admin/whispers/reviews/"+task.ID, admin.Token, nil, nil, 200)
		request("POST", "/admin/whispers/reviews/"+task.ID+"/decision", admin.Token, moderation.WhisperReviewCommand{ExpectedVersion: task.Version, ExpectedContentVersion: task.ContentVersion, Decision: "approved"}, nil, 200)
	}
	approve("whisper_post", post.ID)
	var public whisper.PublicPost
	request("GET", "/whispers/posts/"+post.ID, "", nil, &public, 200)
	if public.Body != "本地原文人工审核" {
		t.Fatal("人工审核改写了原文")
	}
	var capsule whisper.Capsule
	request("POST", "/me/whispers/capsules", token, whisper.CapsuleInput{Body: "本人胶囊原文", UnlockAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), SafetyNoticeAccepted: true}, &capsule, 202)
	request("GET", "/me/whispers/capsules/"+capsule.ID, token, nil, &capsule, 200)
	if capsule.Body != nil {
		t.Fatal("提前泄漏胶囊")
	}
	approve("whisper_capsule", capsule.ID)
	must(root.Exec("UPDATE whisper_capsules SET created_at=clock_timestamp()-interval '2 minutes',unlock_at=date_trunc('second',clock_timestamp())-interval '1 second' WHERE id=?", capsule.ID).Error)
	request("GET", "/me/whispers/capsules/"+capsule.ID, token, nil, &capsule, 200)
	if capsule.Body == nil || *capsule.Body != "本人胶囊原文" {
		t.Fatal("到期胶囊读取失败")
	}
	var account entity.Account
	must(root.Where("id=?", actor).Take(&account).Error)
	request("POST", "/me/deletion-requests", token, privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: account.Version}, nil, 202)
	request("GET", "/me", token, nil, nil, 401)
	if e = app.Ready(ctx); e != nil {
		t.Fatal("已装配API基础检查失败", e)
	}
	// 文件日志保持目标屏障，恢复清理完成后才能新开读取进程。
	_, e = OpenAPI(ctx, cfg, client, log)
	if e == nil {
		t.Fatal("本地日志恢复门禁被绕过")
	}
	cancel()
	must(<-done)
	stopped = true
	must(RunPrivacy(ctx, path, "integration", "account", true, log))
	rebuilt, e := OpenAPI(ctx, cfg, client, log)
	must(e)
	must(rebuilt.Close())
	if bin := os.Getenv("APP_WHISPER_SERVER_BINARY"); bin != "" {
		yaml := filepath.Join(dir, "server.yaml")
		must(os.WriteFile(yaml, []byte("environment: integration\ndevappfile: "+devPath+"\nwhispersfile: "+path+"\nredis:\n  enabled: true\n  address: "+redisAddress+"\ndatabase:\n  driver: postgres\n"), 0600))
		cmd := exec.Command(bin, "-config", yaml, "-check")
		cmd.Env = []string{}
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "APP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		raw, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("本地实际server诊断失败: %s", raw)
		}
	}
	t.Log("单DSN组合API、真实PG/Redis、岗位隔离、原文人工审核、本人胶囊及本地删除重启门禁通过；身份为合成fixture")
}
