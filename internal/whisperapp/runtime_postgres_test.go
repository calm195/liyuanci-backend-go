package whisperapp

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

var moderationCareFixture = moderation.CareResources{Version: "synthetic-v1", Source: "隔离合成资料", VerifiedAt: "2026-10-28", Hotline24Hours: "隔离合成联系占位", Appointment: "隔离合成预约", Notice: "仅用于测试，不是真实援助资料"}

type processLog struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (l *processLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.Write(b)
}
func (l *processLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.data.String() }

type runtimeProcess struct {
	cmd  *exec.Cmd
	log  *processLog
	done chan error
	once sync.Once
}

func (p *runtimeProcess) stop(t *testing.T) {
	t.Helper()
	p.once.Do(func() {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case e := <-p.done:
			if e != nil {
				t.Errorf("进程未优雅退出：%v", e)
			}
		case <-time.After(15 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
			t.Error("进程停止超时")
		}
	})
}

// TestPostgresWhisperRuntime 验证B08真实二进制、受限登录身份、TLS供应商、Redis、MQ/补偿和恢复启动门禁。
// TLS供应商、KMS、文本处理与全副本清理为隔离合成，不代表生产供应商及灾备验收。
func TestPostgresWhisperRuntime(t *testing.T) {
	dsn := os.Getenv("APP_WHISPER_TEXT_TEST_DSN")
	redisAddress := os.Getenv("APP_WHISPER_REDIS_TEST_ADDRESS")
	serverBin, contentBin, privacyBin := os.Getenv("APP_WHISPER_SERVER_BINARY"), os.Getenv("APP_WHISPER_CONTENT_BINARY"), os.Getenv("APP_WHISPER_PRIVACY_BINARY")
	if dsn == "" || redisAddress == "" || serverBin == "" || contentBin == "" || privacyBin == "" {
		t.Skip("需显式隔离PG/Redis及三个实际进程，make integration覆盖")
	}
	ctx, log := context.Background(), zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal("运行装配验证失败", e)
		}
	}
	open := func(value string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		must(e)
		pool, e := db.DB()
		must(e)
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	parent := open(dsn)
	var parentName string
	must(parent.Raw("SELECT current_database()").Scan(&parentName).Error)
	needle := "dbname=" + parentName + " "
	if !strings.Contains(dsn, needle) || (parentName != "liyuanci" && parentName != "whisper_test") {
		t.Fatal("需显式隔离父库")
	}
	name := "whisper_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(parent.Exec("CREATE DATABASE " + name).Error)
	must(parent.Exec("CREATE DATABASE " + name + "_journal").Error)
	businessDSN := strings.Replace(dsn, needle, "dbname="+name+" ", 1)
	journalDSN := strings.Replace(dsn, needle, "dbname="+name+"_journal ", 1)
	root, journalRoot := open(businessDSN), open(journalDSN)
	must(migration.Run(ctx, root, log, "up"))
	must(migration.ApplyPermissions(ctx, root, log))
	must(migration.RunJournal(ctx, journalRoot, log, "up"))
	must(migration.RunJournal(ctx, journalRoot, log, "permissions"))
	login := func(base, role string) string {
		username := "runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		password := uuid.NewString()
		must(parent.Exec("CREATE ROLE " + username + " LOGIN PASSWORD '" + password + "' IN ROLE " + role).Error)
		// DSN格式由固定隔离fixture生成，不保留管理员身份或SET ROLE绕过登录检查。
		parts := strings.Fields(base)
		for i, v := range parts {
			if strings.HasPrefix(v, "user=") {
				parts[i] = "user=" + username
			}
			if strings.HasPrefix(v, "password=") {
				parts[i] = "password=" + password
			}
		}
		return strings.Join(parts, " ")
	}
	cfg := testSettings()
	cfg.APIDSN = login(businessDSN, "liyuanci_api")
	cfg.ReviewDSN = login(businessDSN, "liyuanci_admin_api")
	cfg.CrisisDSN = login(businessDSN, "liyuanci_crisis_api")
	cfg.PrivacyDSN = login(businessDSN, "liyuanci_privacy_maintenance")
	cfg.EvidenceDeletionDSN = login(businessDSN, "liyuanci_evidence_delete_api")
	cfg.WorkerDSN = login(businessDSN, "liyuanci_domain_worker")
	cfg.EvidencePurgerDSN = login(businessDSN, "liyuanci_evidence_purger")
	cfg.RecoveryWriterDSN = login(journalDSN, "liyuanci_recovery_writer")
	cfg.RecoveryReaderDSN = login(journalDSN, "liyuanci_recovery_reader")
	cfg.Command.Timeout = 5 * time.Second
	cfg.Command.LockTimeout = time.Second
	cfg.Worker.Consumer.Timeout = 3 * time.Second
	cfg.Worker.Consumer.LockTimeout = time.Second
	cfg.Worker.AnalysisTimeout = 3 * time.Second
	cfg.Worker.Consumer.RetryDelay = 50 * time.Millisecond
	cfg.ProviderTimeout = 3 * time.Second
	cfg.OperationTimeout = 20 * time.Second
	cfg.Interval = 50 * time.Millisecond
	dir := t.TempDir()
	write := func(name string, value any) string {
		t.Helper()
		path := filepath.Join(dir, name)
		raw, e := json.Marshal(value)
		must(e)
		must(os.WriteFile(path, raw, 0600))
		return path
	}
	var outage atomic.Bool
	gatewayServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-runtime-gateway-token" {
			w.WriteHeader(401)
			return
		}
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		var req struct {
			Operation, Version string
			Input              json.RawMessage
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Version != cfg.ProviderVersion {
			w.WriteHeader(400)
			return
		}
		respond := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		switch req.Operation {
		case "health":
			respond(map[string]any{"Version": cfg.ProviderVersion, "Capabilities": []string{"wrap", "unwrap", "public_analysis", "private_classify", "redact", "erase", "notify"}})
		case "wrap", "unwrap":
			var in struct {
				Scope, KeyRef string
				Key, Wrapped  []byte
			}
			if json.Unmarshal(req.Input, &in) != nil {
				w.WriteHeader(400)
				return
			}
			material := byte(101)
			if in.Scope == "recovery" {
				material = 102
			}
			block, _ := aes.NewCipher(bytes.Repeat([]byte{material}, 32))
			gcm, _ := cipher.NewGCM(block)
			if req.Operation == "wrap" {
				nonce := make([]byte, gcm.NonceSize())
				_, _ = rand.Read(nonce)
				respond(map[string]any{"KeyRef": in.KeyRef, "Wrapped": append(nonce, gcm.Seal(nil, nonce, in.Key, nil)...)})
				clear(in.Key)
			} else {
				if len(in.Wrapped) < gcm.NonceSize() {
					w.WriteHeader(400)
					return
				}
				key, e := gcm.Open(nil, in.Wrapped[:gcm.NonceSize()], in.Wrapped[gcm.NonceSize():], nil)
				if e != nil {
					w.WriteHeader(400)
					return
				}
				respond(map[string]any{"Key": key})
				clear(key)
			}
		case "public_analysis":
			var in struct {
				Kind, Body string
				Identity   whisper.IdentityInput
			}
			_ = json.Unmarshal(req.Input, &in)
			respond(map[string]any{"Body": strings.ReplaceAll(in.Body, "合成人名", "[已脱敏]"), "Identity": in.Identity, "SchoolRelated": false, "OrdinaryHit": false, "CrisisHit": false, "DetectorVersion": "synthetic-runtime-public"})
		case "private_classify":
			respond(map[string]any{"OrdinaryHit": false, "CrisisHit": false, "DetectorVersion": "synthetic-runtime-private"})
		case "redact":
			respond(map[string]any{"Body": "受控脱敏后的作者自有内容"})
		case "erase":
			respond(map[string]any{"Completed": true, "WriteBarrierConfirmed": true})
		case "notify":
			respond(map[string]any{"Completed": true})
		default:
			w.WriteHeader(400)
		}
	}))
	defer gatewayServer.Close()
	cfg.GatewayURL = gatewayServer.URL
	cfg.GatewayTokenFile = filepath.Join(dir, "gateway.token")
	must(os.WriteFile(cfg.GatewayTokenFile, []byte("synthetic-runtime-gateway-token"), 0600))
	cfg.GatewayCAFile = filepath.Join(dir, "gateway.pem")
	must(os.WriteFile(cfg.GatewayCAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gatewayServer.Certificate().Raw}), 0600))
	cfg.CatalogFile = write("catalog.json", ResourcesFile{Version: "synthetic-runtime-resources-v1", Catalog: whisper.CatalogSnapshot{Grades: whisper.Dictionary{Items: []whisper.Item{}}, Help: whisper.HelpDirectory{Status: "unavailable", Items: []whisper.HelpItem{}}}, Care: &moderationCareFixture})
	for _, kind := range []string{"crisis", "school", "ordinary_violation"} {
		id := uuid.NewString()
		must(root.Create(&entity.WordlistVersion{ID: id, Type: kind, Version: 1, Status: "published"}).Error)
		must(root.Exec("INSERT INTO public.wordlist_heads(kind,version,effective_id) VALUES (?,1,?) ON CONFLICT(kind) DO UPDATE SET version=EXCLUDED.version,effective_id=EXCLUDED.effective_id", kind, id).Error)
	}
	must(blockpolicy.Enable(ctx, root, log))
	mq := os.Getenv("APP_BUSINESS_TEST_NAMESERVER")
	if mq != "" {
		cfg.DatabasePolling = false
		suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
		for i, item := range []struct {
			path         *string
			topic, event string
		}{{&cfg.ReviewMQFile, "liyuanci_whisper_review_v1", "whisper.review.requested"}, {&cfg.UnlockMQFile, "liyuanci_whisper_unlock_v1", "whisper.capsule.unlock.requested"}} {
			c := workrunner.Config{}
			c.MQ.NameServers = []string{mq}
			c.MQ.Topic = item.topic
			c.MQ.EventType = item.event
			c.MQ.ProducerGroup = fmt.Sprintf("runtime_relay_%s_%d", suffix, i)
			c.MQ.ConsumerGroup = fmt.Sprintf("runtime_scan_%s_%d", suffix, i)
			c.MQ.Instance = fmt.Sprintf("runtime_%s_%d", suffix, i)
			c.MQ.AllowUnauthenticated = true
			c.MQ.SendTimeout = time.Second
			c.MQ.HandlerTimeout = cfg.OperationTimeout
			c.MQ.Concurrency = 1
			c.MQ.MaxReconsume = 2
			c.MQ.RetryDelayLevel = 1
			c.Relay.DatabaseTimeout = 3 * time.Second
			c.Relay.SendTimeout = time.Second
			c.Relay.LeaseDuration = 5 * time.Second
			c.Relay.RetryDelay = time.Second
			c.Relay.MaxAttempts = 3
			c.RelayInterval = 50 * time.Millisecond
			c.ScanInterval = 30 * time.Second
			c.OperationTimeout = cfg.OperationTimeout
			// LoadCardWorkerMQ接受JSON作为YAML子集，期限使用数值纳秒，与私有配置保持相同格式。
			*item.path = write(fmt.Sprintf("mq-%d.json", i), c)
		}
	}
	configPath := write("runtime.json", cfg)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	must(e)
	address := listener.Addr().String()
	must(listener.Close())
	appPath := filepath.Join(dir, "server.yaml")
	must(os.WriteFile(appPath, []byte("environment: integration\nwhispersfile: "+configPath+"\nserver:\n  address: "+address+"\ndatabase:\n  driver: postgres\nredis:\n  enabled: true\n  address: "+redisAddress+"\n"), 0600))
	environment := func() []string {
		out := []string{}
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "APP_") {
				out = append(out, v)
			}
		}
		return append(out, "APP_ENVIRONMENT=integration", "APP_WHISPERSFILE="+configPath, "APP_PRIVACY_SCOPE=account", "APP_PRIVACY_RECOVER=true")
	}
	start := func(binary string, args ...string) *runtimeProcess {
		t.Helper()
		p := &runtimeProcess{cmd: exec.Command(binary, args...), log: &processLog{}, done: make(chan error, 1)}
		p.cmd.Env = environment()
		p.cmd.Stdout = p.log
		p.cmd.Stderr = p.log
		must(p.cmd.Start())
		go func() { p.done <- p.cmd.Wait() }()
		t.Cleanup(func() { p.stop(t) })
		return p
	}
	wait := func(name string, fn func() bool) {
		t.Helper()
		limit := time.Now().Add(40 * time.Second)
		for time.Now().Before(limit) {
			if fn() {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("等待超时：", name)
	}
	oneShot := func(binary string, wantSuccess bool, args ...string) {
		t.Helper()
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(call, binary, args...)
		cmd.Env = environment()
		raw, e := cmd.CombinedOutput()
		if (e == nil) != wantSuccess {
			t.Fatalf("进程结果与预期不符：success=%v 输出=%s", wantSuccess, string(raw))
		}
		if bytes.Contains(raw, []byte("synthetic-runtime-gateway-token")) {
			t.Fatal("日志泄漏供应商凭据")
		}
	}
	actor, peer := uuid.NewString(), uuid.NewString()
	token, peerToken := uuid.NewString(), uuid.NewString()
	for _, row := range [][2]string{{actor, token}, {peer, peerToken}} {
		must(root.Create(&entity.Account{ID: row[0], Nickname: "隔离合成账号", Verification: "verified"}).Error)
		hash := sha256.Sum256([]byte(row[1]))
		must(root.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), row[0], hash[:]).Error)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path, tok string, body any, status int, out any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, e := http.NewRequest(method, "http://"+address+"/api/v1"+path, bytes.NewReader(raw))
		must(e)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		response, e := client.Do(req)
		must(e)
		defer response.Body.Close()
		var envelope struct {
			Data      json.RawMessage
			ErrorCode string
		}
		must(json.NewDecoder(response.Body).Decode(&envelope))
		if response.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, envelope.ErrorCode)
		}
		if out != nil {
			must(json.Unmarshal(envelope.Data, out))
		}
	}
	server := start(serverBin, "-config", appPath)
	wait("正式API监听", func() bool {
		response, e := client.Get("http://" + address + "/healthz")
		if e != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == 200
	})
	checkHTTP := func(path string, status int) {
		t.Helper()
		response, e := client.Get("http://" + address + path)
		must(e)
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatal(path, response.StatusCode)
		}
	}
	checkHTTP("/readyz", 200)
	var module whisper.ModuleConfig
	request("GET", "/whispers/config", "", nil, 200, &module)
	if !module.Enabled || module.SemiAvailable {
		t.Fatal("资料缺失边界错误")
	}
	content := start(contentBin)
	wait("内容调度启动", func() bool { return strings.Contains(content.log.String(), "树洞内容进程启动完成") })
	createPost := func(body string) whisper.OwnerPost {
		var post whisper.OwnerPost
		request("POST", "/me/whispers/posts", token, whisper.ContentInput{Body: body, Identity: whisper.IdentityInput{Mode: "anonymous"}}, 201, &post)
		request("POST", "/me/whispers/posts/"+post.ID+"/submit", token, map[string]any{"expected_version": post.Version}, 202, &post)
		return post
	}
	published := func(id string) {
		wait("正式审核消费", func() bool {
			var n int64
			return root.Model(&entity.WhisperPost{}).Where("id=? AND published_content_version IS NOT NULL", id).Count(&n).Error == nil && n == 1
		})
	}
	p := createPost("合成人名 的运行装配验证")
	published(p.ID)
	var public whisper.PublicPost
	request("GET", "/whispers/posts/"+p.ID, "", nil, 200, &public)
	if strings.Contains(public.Body, "合成人名") {
		t.Fatal("公开原文未脱敏")
	}
	var comment whisper.OwnerComment
	request("POST", "/whispers/posts/"+p.ID+"/comments", peerToken, whisper.CommentInput{ContentInput: whisper.ContentInput{Body: "作者自有的合成暖评", Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedPostVersion: public.Version, ExpectedCommentsEpoch: public.CommentsEpoch}, 202, &comment)
	wait("暖评最终公开", func() bool {
		var n int64
		return root.Model(&entity.WhisperComment{}).Where("id=? AND review_state='approved'", comment.ID).Count(&n).Error == nil && n == 1
	})
	var capsule whisper.Capsule
	request("POST", "/me/whispers/capsules", token, whisper.CapsuleInput{Body: "私密原文保持不变", UnlockAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), SafetyNoticeAccepted: true}, 202, &capsule)
	wait("胶囊私密分类", func() bool {
		var n int64
		return root.Model(&entity.WhisperCapsule{}).Where("id=? AND review_state='approved'", capsule.ID).Count(&n).Error == nil && n == 1
	})
	must(root.Exec("UPDATE public.whisper_capsules SET created_at=clock_timestamp()-interval '2 minutes',unlock_at=date_trunc('second',clock_timestamp())-interval '1 second' WHERE id=?", capsule.ID).Error)
	must(root.Exec("UPDATE public.work_items SET next_at=clock_timestamp() WHERE target_id=? AND kind='whisper_capsule_unlock'", capsule.ID).Error)
	must(root.Exec("UPDATE public.outbox_events SET next_at=clock_timestamp() WHERE aggregate_id=? AND event_type='whisper.capsule.unlock.requested'", capsule.ID).Error)
	wait("正式解锁调度通知", func() bool {
		var n int64
		return root.Model(&entity.WhisperNotificationRef{}).Where("source_id=?", capsule.ID).Count(&n).Error == nil && n == 1
	})
	request("GET", "/me/whispers/capsules/"+capsule.ID, token, nil, 200, &capsule)
	if capsule.Body == nil || *capsule.Body != "私密原文保持不变" {
		t.Fatal("私密分类替换原文")
	}
	if mq != "" {
		wait("真实MQ发送与消费确认", func() bool {
			var count int64
			return strings.Contains(content.log.String(), "消息确认决策完成") && root.Model(&entity.OutboxEvent{}).Where("aggregate_id=? AND event_type='whisper.capsule.unlock.requested' AND sent_at IS NOT NULL", capsule.ID).Count(&count).Error == nil && count == 1
		})
	}
	outage.Store(true)
	checkHTTP("/readyz", 503)
	checkHTTP("/healthz", 200)
	outage.Store(false)
	content.stop(t)
	pending := createPost("停机期间受理的合成稿")
	content = start(contentBin)
	wait("补偿进程启动", func() bool { return strings.Contains(content.log.String(), "树洞内容进程启动完成") })
	published(pending.ID)
	// 关闭模块只改变普通能力，不回退旧黑名单保护；密钥与历史引用保持原值。
	server.stop(t)
	cfg.Enabled = false
	must(os.WriteFile(configPath, mustJSON(t, cfg), 0600))
	server = start(serverBin, "-config", appPath)
	wait("关闭模块重启", func() bool {
		response, e := client.Get("http://" + address + "/healthz")
		if e != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == 200
	})
	request("GET", "/whispers/config", "", nil, 200, &module)
	if module.Enabled {
		t.Fatal("模块未关闭")
	}
	request("GET", "/whispers/posts/"+p.ID, "", nil, 503, nil)
	request("GET", "/me/blocks", peerToken, nil, 409, nil)
	request("GET", "/me/interaction-blocks", peerToken, nil, 200, nil)
	// 启动诊断不能绕过独立日志中仍存在的删除目标。
	var a entity.Account
	must(root.Where("id=?", actor).Take(&a).Error)
	var accepted privacy.Accepted
	request("POST", "/me/deletion-requests", token, privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: a.Version}, 202, &accepted)
	server.stop(t)
	content.stop(t)
	oneShot(serverBin, false, "-config", appPath, "-check")
	oneShot(privacyBin, true)
	oneShot(serverBin, true, "-config", appPath, "-check")
	// 模拟恢复出已确认删除的主体；读取进程必须再次拒绝，隐私进程重删后才可启动。
	must(root.Create(&entity.Account{ID: actor, Nickname: "恢复合成残留"}).Error)
	oneShot(serverBin, false, "-config", appPath, "-check")
	oneShot(privacyBin, true)
	oneShot(serverBin, true, "-config", appPath, "-check")
	cfg.Enabled = true
	must(os.WriteFile(configPath, mustJSON(t, cfg), 0600))
	server = start(serverBin, "-config", appPath)
	wait("恢复重删后API", func() bool {
		response, e := client.Get("http://" + address + "/healthz")
		if e != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == 200
	})
	request("GET", "/me/whispers/comments/"+comment.ID, peerToken, nil, 200, &comment)
	if comment.Body != nil || comment.PostID != nil || comment.RecoveryBody == nil {
		t.Fatal("正式隐私进程未产生安全恢复候选")
	}
	server.stop(t)
	// 管理员池、未迁移库和供应商缺失不会退回开发适配器或触发DDL。
	cfg.APIDSN = businessDSN
	must(os.WriteFile(configPath, mustJSON(t, cfg), 0600))
	oneShot(serverBin, false, "-config", appPath, "-check")
	var version int64
	must(root.Raw("SELECT max(version) FROM public.schema_migrations").Scan(&version).Error)
	if version != 26 {
		t.Fatal("运行入口改变迁移版本", version)
	}
	t.Logf("正式server/content-worker/privacy-worker、受限登录、TLS、Redis、MQ=%v、禁用/重启/恢复门禁通过；供应商/KMS/清理为合成", mq != "")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
