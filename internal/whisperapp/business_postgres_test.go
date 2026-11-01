package whisperapp

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/verification"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

// TestPostgresBusinessRuntime 使用真实生产API二进制、受限角色、PG/Redis和可选鉴权MQ。
// 微信主体、学校规则、扫描、KMS、通知和对象清理均由隔离TLS夹具替代，不是正式供应商验收。
func TestPostgresBusinessRuntime(t *testing.T) {
	testPostgresBusinessRuntime(t, false)
}

// TestPostgresBusinessDatabaseRuntime 使用生产数据库任务通道与收紧的连接池，完全不构造MQ客户端。
func TestPostgresBusinessDatabaseRuntime(t *testing.T) {
	testPostgresBusinessRuntime(t, true)
}

func testPostgresBusinessRuntime(t *testing.T, databaseTasks bool) {
	dsn, redisAddress := os.Getenv("APP_WHISPER_TEXT_TEST_DSN"), os.Getenv("APP_BUSINESS_RUNTIME_REDIS_ADDRESS")
	redisPassword := os.Getenv("APP_BUSINESS_RUNTIME_REDIS_PASSWORD")
	serverBin, workerBin, initBin := os.Getenv("APP_WHISPER_SERVER_BINARY"), os.Getenv("APP_BUSINESS_RUNTIME_WORKER_BINARY"), os.Getenv("APP_BUSINESS_RUNTIME_INIT_BINARY")
	if dsn == "" || redisAddress == "" || redisPassword == "" || serverBin == "" || workerBin == "" || initBin == "" {
		t.Skip("需隔离PG、鉴权Redis及实际业务二进制，make integration覆盖")
	}
	ctx, log := context.Background(), zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal("完整业务装配验收失败", e)
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
	var parentName string
	must(parent.Raw("SELECT current_database()").Scan(&parentName).Error)
	needle := "dbname=" + parentName + " "
	if !strings.Contains(dsn, needle) || parentName != "liyuanci" && parentName != "whisper_test" {
		t.Fatal("要求显式隔离父库")
	}
	name := "business_release_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
		user, pass := "release_"+strings.ReplaceAll(uuid.NewString(), "-", ""), uuid.NewString()
		must(parent.Exec("CREATE ROLE " + user + " LOGIN PASSWORD '" + pass + "' IN ROLE " + role).Error)
		parts := strings.Fields(base)
		for i, v := range parts {
			if strings.HasPrefix(v, "user=") {
				parts[i] = "user=" + user
			}
			if strings.HasPrefix(v, "password=") {
				parts[i] = "password=" + pass
			}
		}
		return strings.Join(parts, " ")
	}
	c := testBusinessSettings(t)
	if databaseTasks {
		c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 2, 1
	}
	c.Runtime.APIDSN = login(businessDSN, "liyuanci_api")
	c.Runtime.ReviewDSN = login(businessDSN, "liyuanci_admin_api")
	c.Runtime.CrisisDSN = login(businessDSN, "liyuanci_crisis_api")
	c.Runtime.PrivacyDSN = login(businessDSN, "liyuanci_privacy_maintenance")
	c.Runtime.EvidenceDeletionDSN = login(businessDSN, "liyuanci_evidence_delete_api")
	c.Runtime.WorkerDSN = login(businessDSN, "liyuanci_domain_worker")
	c.Runtime.EvidencePurgerDSN = login(businessDSN, "liyuanci_evidence_purger")
	c.Runtime.RecoveryWriterDSN = login(journalDSN, "liyuanci_recovery_writer")
	c.Runtime.RecoveryReaderDSN = login(journalDSN, "liyuanci_recovery_reader")
	c.StudentDSN = login(businessDSN, "liyuanci_identity_maintenance")
	c.AdminDSN = login(businessDSN, "liyuanci_admin_maintenance")
	c.Runtime.Command.Timeout = 5 * time.Second
	c.Runtime.Command.LockTimeout = time.Second
	c.Runtime.Worker.Consumer.Timeout = 3 * time.Second
	c.Runtime.Worker.Consumer.LockTimeout = time.Second
	c.Runtime.Worker.Consumer.RetryDelay = 50 * time.Millisecond
	c.Runtime.Worker.AnalysisTimeout = 3 * time.Second
	c.Runtime.ProviderTimeout = 3 * time.Second
	c.Runtime.OperationTimeout = 60 * time.Second
	c.Runtime.Interval = 50 * time.Millisecond
	if databaseTasks {
		c.Runtime.Worker.Consumer.RetryDelay = time.Second
		c.Card.RetryDelay = time.Second
	}
	dir := t.TempDir()
	write := func(name string, v any) string {
		t.Helper()
		p := filepath.Join(dir, name)
		must(os.WriteFile(p, mustJSON(t, v), 0600))
		return p
	}
	var outage, eraseFailure, imageFailure atomic.Bool
	var imageCalls, eraseCalls atomic.Int64
	var objectsMu sync.Mutex
	objects := map[string][]byte{}
	providerVersion := c.Runtime.ProviderVersion
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-business-gateway-token" {
			w.WriteHeader(401)
			return
		}
		var req struct {
			Operation, Version string
			Input              json.RawMessage
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Version != providerVersion {
			w.WriteHeader(400)
			return
		}
		respond := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		switch req.Operation {
		case "health":
			respond(map[string]any{"Version": providerVersion, "Capabilities": []string{"wrap", "unwrap", "public_analysis", "private_classify", "redact", "erase", "notify", "identity_digest", "object_put", "object_open", "image_scan", "school_validate", "content_analysis"}})
		case "wrap", "unwrap":
			var in struct {
				Scope, KeyRef string
				Key, Wrapped  []byte
			}
			_ = json.Unmarshal(req.Input, &in)
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
		case "identity_digest":
			var in struct {
				KeyRef, Purpose string
				Value           []byte
			}
			_ = json.Unmarshal(req.Input, &in)
			h := hmac.New(sha256.New, bytes.Repeat([]byte{103}, 32))
			_, _ = h.Write([]byte(in.KeyRef + ":" + in.Purpose))
			_, _ = h.Write(in.Value)
			respond(map[string]any{"Digest": h.Sum(nil)})
		case "object_put":
			var in struct {
				Key, SHA256 string
				Data        []byte
			}
			_ = json.Unmarshal(req.Input, &in)
			objectsMu.Lock()
			defer objectsMu.Unlock()
			if old, ok := objects[in.Key]; ok && !bytes.Equal(old, in.Data) {
				w.WriteHeader(409)
				return
			}
			objects[in.Key] = bytes.Clone(in.Data)
			respond(map[string]any{"Version": "immutable-v1", "SHA256": in.SHA256})
		case "object_open":
			var in struct{ Key, Version string }
			_ = json.Unmarshal(req.Input, &in)
			objectsMu.Lock()
			defer objectsMu.Unlock()
			respond(map[string]any{"Version": "immutable-v1", "Data": objects[in.Key]})
		case "image_scan":
			imageCalls.Add(1)
			if imageFailure.Load() {
				w.WriteHeader(503)
				return
			}
			respond(map[string]any{"Approved": true, "DetectorVersion": "synthetic-image-v1"})
		case "school_validate":
			respond(map[string]any{"Valid": true, "RulesVersion": "synthetic-school-v1"})
		case "public_analysis":
			var in struct {
				Body     string
				Identity whisper.IdentityInput
			}
			_ = json.Unmarshal(req.Input, &in)
			respond(map[string]any{"Body": in.Body, "Identity": in.Identity, "DetectorVersion": "synthetic-public-v1"})
		case "content_analysis":
			var in struct {
				Body         string
				IdentityTags any
			}
			_ = json.Unmarshal(req.Input, &in)
			respond(map[string]any{"Body": in.Body, "Tags": in.IdentityTags, "DetectorVersion": "synthetic-content-v1"})
		case "private_classify":
			respond(map[string]any{"DetectorVersion": "synthetic-private-v1"})
		case "redact":
			respond(map[string]any{"Body": "原内容已移除，请重新编辑。"})
		case "erase":
			eraseCalls.Add(1)
			respond(map[string]any{"Completed": !eraseFailure.Load(), "WriteBarrierConfirmed": !eraseFailure.Load()})
		case "notify":
			respond(map[string]any{"Completed": true})
		default:
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(tlsServer.Close)
	c.Runtime.GatewayURL = tlsServer.URL
	c.Runtime.GatewayTokenFile = filepath.Join(dir, "gateway.token")
	must(os.WriteFile(c.Runtime.GatewayTokenFile, []byte("synthetic-business-gateway-token"), 0600))
	c.Runtime.GatewayCAFile = filepath.Join(dir, "ca.pem")
	must(os.WriteFile(c.Runtime.GatewayCAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}), 0600))
	version, verified := int64(1), "2026-10-31T00:00:00Z"
	c.Runtime.CatalogFile = write("catalog.json", ResourcesFile{Version: "synthetic-release-v1", Care: &moderationCareFixture, Catalog: whisper.CatalogSnapshot{Grades: whisper.Dictionary{Version: &version, Items: []whisper.Item{{Code: "grade_test", Label: "隔离合成年级"}}}, Reports: []whisper.Item{{Code: "other", Label: "隔离合成举报"}}, Help: whisper.HelpDirectory{Status: "ready", Version: &version, VerifiedAt: &verified, Items: []whisper.HelpItem{{Kind: "school_appointment", Label: "隔离合成援助", Contact: "仅用于测试，非真实联系渠道", SourceURL: "https://example.invalid/test"}}}}})
	must(root.Exec("INSERT INTO public.report_types(code,label,published) VALUES ('other','隔离合成举报',true)").Error)
	workerEnv := "integration"
	if mq := os.Getenv("APP_BUSINESS_TEST_NAMESERVER"); mq != "" && !databaseTasks {
		c.Runtime.DatabasePolling = false
		access, secret := os.Getenv("APP_BUSINESS_RUNTIME_MQ_ACCESS_KEY"), os.Getenv("APP_BUSINESS_RUNTIME_MQ_SECRET_KEY")
		if access != "" && secret != "" {
			workerEnv = "production"
		}
		for _, kind := range []string{"card", "content", "whispers-review", "whispers-unlock"} {
			var options workrunner.Config
			must(json.Unmarshal([]byte(`{}`), &options))
			// 公共模板不含凭据；各测试组使用随机消费者组，避免前序测试历史影响结果。
			must(loadMQTemplate(kind, &options))
			options.MQ.NameServers = []string{mq}
			options.MQ.Credentials.AccessKey = access
			options.MQ.Credentials.SecretKey = secret
			options.MQ.AllowUnauthenticated = workerEnv != "production"
			suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
			options.MQ.ProducerGroup = "release_p_" + suffix
			options.MQ.ConsumerGroup = "release_c_" + suffix
			options.MQ.Instance = "release_i_" + suffix
			options.ScanInterval = time.Second
			if workerEnv == "production" {
				// 鉴权验收延后周期补偿，让新任务必须经消息投递唤醒。
				options.ScanInterval = time.Minute
			}
			options.RelayInterval = 50 * time.Millisecond
			p := write(kind+"-mq.json", options)
			switch kind {
			case "card":
				c.CardMQFile = p
			case "content":
				c.ContentMQFile = p
			case "whispers-review":
				c.Runtime.ReviewMQFile = p
			case "whispers-unlock":
				c.Runtime.UnlockMQFile = p
			}
		}
	}
	if databaseTasks {
		c.Runtime.DatabasePolling = false
		c.Runtime.TaskTransport = "postgres"
		workerEnv = "production"
	}
	workerPath := write("worker.json", c)
	apiCfg := c
	apiCfg.Runtime.DatabasePolling = false
	apiPath := write("api.json", apiCfg)
	redisFile := filepath.Join(dir, "redis.password")
	must(os.WriteFile(redisFile, []byte(redisPassword), 0600))
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	must(e)
	address := listener.Addr().String()
	must(listener.Close())
	appPath := filepath.Join(dir, "app.yaml")
	must(os.WriteFile(appPath, []byte(fmt.Sprintf("environment: production\nbusinessfile: %s\nserver:\n  address: %s\ndatabase:\n  driver: postgres\nredis:\n  enabled: true\n  address: %s\n  passwordfile: %s\n", apiPath, address, redisAddress, redisFile)), 0600))
	cleanEnv := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "APP_") {
			cleanEnv = append(cleanEnv, v)
		}
	}
	oneShot := func(bin string, success bool, args ...string) {
		t.Helper()
		call, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(call, bin, args...)
		cmd.Env = cleanEnv
		raw, e := cmd.CombinedOutput()
		if (e == nil) != success {
			t.Fatalf("命令结果不符 success=%v: %s", success, raw)
		}
		if bytes.Contains(raw, []byte("synthetic-business-gateway-token")) {
			t.Fatal("凭据进入日志")
		}
	}
	start := func(bin string, args ...string) *runtimeProcess {
		t.Helper()
		p := &runtimeProcess{cmd: exec.Command(bin, args...), log: &processLog{}, done: make(chan error, 1)}
		p.cmd.Env = cleanEnv
		p.cmd.Stdout = p.log
		p.cmd.Stderr = p.log
		must(p.cmd.Start())
		go func() { p.done <- p.cmd.Wait() }()
		t.Cleanup(func() {
			p.stop(t)
			if t.Failed() {
				t.Logf("进程 %s 日志：%s", filepath.Base(bin), p.log.String())
			}
		})
		return p
	}
	wait := func(name string, check func() bool, processes ...*runtimeProcess) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(40 * time.Millisecond)
		}
		for _, p := range processes {
			t.Log(p.log.String())
		}
		t.Fatal("等待超时", name)
	}
	words := write("wordlists.json", map[string][]string{"crisis": {"隔离危机词"}, "school": {"隔离涉校词"}, "ordinary_violation": {"隔离违规词"}})
	oneShot(initBin, true, "-config", appPath, "-check-config")
	oneShot(serverBin, false, "-config", appPath, "-check")
	oneShot(initBin, true, "-config", appPath, "-login", "release-admin", "-wordlists", words)
	oneShot(initBin, true, "-config", appPath, "-login", "release-admin", "-wordlists", words)
	must(blockpolicy.Enable(ctx, root, log))
	oneShot(serverBin, true, "-config", appPath, "-check-config")
	oneShot(serverBin, true, "-config", appPath, "-check")
	server := start(serverBin, "-config", appPath)
	client := &http.Client{Timeout: 10 * time.Second}
	health := func(path string) int {
		r, e := client.Get("http://" + address + path)
		if e != nil {
			return 0
		}
		_ = r.Body.Close()
		return r.StatusCode
	}
	wait("生产完整API", func() bool { return health("/readyz") == 200 }, server)
	request := func(method, path, token string, body any, status int, out any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, e := http.NewRequest(method, "http://"+address+"/api/v1"+path, bytes.NewReader(raw))
		must(e)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, e := client.Do(req)
		must(e)
		defer resp.Body.Close()
		var envelope struct {
			Data      json.RawMessage
			ErrorCode string
		}
		must(json.NewDecoder(resp.Body).Decode(&envelope))
		if resp.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, envelope.ErrorCode)
		}
		if out != nil {
			must(json.Unmarshal(envelope.Data, out))
		}
	}
	var session adminauth.Session
	request("POST", "/admin/sessions", "", map[string]any{"login_name": "release-admin", "password": "synthetic-business-secret"}, 200, &session)
	request("GET", "/admin/users", session.Token, nil, 200, nil)
	request("GET", "/wishes", "", nil, 200, nil)
	request("GET", "/me", "", nil, 401, nil)
	actor, peer := uuid.NewString(), uuid.NewString()
	token, peerToken := uuid.NewString(), uuid.NewString()
	for i, row := range [][2]string{{actor, token}, {peer, peerToken}} {
		state := "verified"
		if i == 0 {
			state = "unverified"
		}
		must(root.Create(&entity.Account{ID: row[0], Nickname: "隔离合成用户", Verification: state}).Error)
		hash := sha256.Sum256([]byte(row[1]))
		must(root.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), row[0], hash[:]).Error)
	}
	request("GET", "/admin/users", token, nil, 403, nil)
	for _, kind := range []string{"card", "content", "whispers", "account", "evidence"} {
		oneShot(workerBin, true, "-config", workerPath, "-environment", workerEnv, "-kind", kind, "-check")
	}
	card := start(workerBin, "-config", workerPath, "-environment", workerEnv, "-kind", "card")
	content := start(workerBin, "-config", workerPath, "-environment", workerEnv, "-kind", "content")
	whispers := start(workerBin, "-config", workerPath, "-environment", workerEnv, "-kind", "whispers")
	var form bytes.Buffer
	multipartWriter := multipart.NewWriter(&form)
	part, e := multipartWriter.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="synthetic.png"`}, "Content-Type": {"image/png"}})
	must(e)
	must(png.Encode(part, image.NewRGBA(image.Rect(0, 0, 4, 4))))
	must(multipartWriter.Close())
	upload, e := http.NewRequest("POST", "http://"+address+"/api/v1/me/student-card-assets", &form)
	must(e)
	upload.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	upload.Header.Set("Authorization", "Bearer "+token)
	upload.Header.Set("Idempotency-Key", uuid.NewString())
	imageFailure.Store(databaseTasks)
	response, e := client.Do(upload)
	must(e)
	var assetEnvelope struct{ Data studentcard.Asset }
	must(json.NewDecoder(response.Body).Decode(&assetEnvelope))
	_ = response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal("学生卡上传", response.StatusCode)
	}
	asset := assetEnvelope.Data
	if databaseTasks {
		wait("图片供应商故障持久重试", func() bool {
			var n int64
			return root.Model(&entity.WorkItem{}).Where("target_id=? AND status='retry_wait' AND attempts>0", asset.AssetID).Count(&n).Error == nil && n == 1
		}, card)
		card.stop(t)
		imageFailure.Store(false)
		card = start(workerBin, "-config", workerPath, "-environment", workerEnv, "-kind", "card")
	}
	wait("真实工作进程调用图片供应商", func() bool {
		request("GET", "/me/student-card-assets/"+asset.AssetID, token, nil, 200, &asset)
		return asset.ScanStatus == "approved"
	}, card)
	if imageCalls.Load() == 0 {
		t.Fatal("扫描未到达正式适配器")
	}
	var verificationResult verification.Result
	request("POST", "/me/verification-requests", token, verification.Input{StudentNumber: "synthetic-number", Name: "隔离合成姓名", AssetID: asset.AssetID}, 202, &verificationResult)
	request("POST", "/admin/identity-reviews/"+verificationResult.RequestID+"/decision", session.Token, verification.Decision{Decision: "approved", ExpectedVersion: verificationResult.Version}, 200, &verificationResult)
	var draft struct {
		ID             string `json:"id"`
		Version        int64  `json:"version"`
		ContentVersion int64  `json:"content_version"`
	}
	request("POST", "/wishes", token, wish.ContentInput{Category: "help", Body: "生产内容工作进程的隔离心愿", Campus: "any"}, 201, &draft)
	request("POST", "/wishes/"+draft.ID+"/submit-review", token, map[string]any{"expected_version": draft.Version, "content_version": draft.ContentVersion}, 202, &draft)
	wait("普通心愿工作进程", func() bool {
		var n int64
		return root.Model(&entity.Wish{}).Where("id=? AND state='ready'", draft.ID).Count(&n).Error == nil && n == 1
	}, content)
	var module whisper.ModuleConfig
	request("GET", "/whispers/config", "", nil, 200, &module)
	if !module.Enabled || !module.SemiAvailable {
		t.Fatal("生产悄悄鲤未完整启用")
	}
	var post whisper.OwnerPost
	var postTask string
	if databaseTasks {
		whispers.stop(t)
	}
	request("POST", "/me/whispers/posts", token, whisper.ContentInput{Body: "生产装配的隔离文字", Identity: whisper.IdentityInput{Mode: "anonymous"}}, 201, &post)
	request("POST", "/me/whispers/posts/"+post.ID+"/submit", token, map[string]any{"expected_version": post.Version}, 202, &post)
	if databaseTasks {
		must(root.Raw("SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry c ON c.id=t.content_id WHERE c.entity_id=? ORDER BY t.created_at DESC LIMIT 1", post.ID).Scan(&postTask).Error)
		// 模拟进程退出后遗留的过期租约；重启继续处理已提交的任务。
		changed := root.Exec("UPDATE public.work_items SET status='leased',lease_until=clock_timestamp()-interval '1 second' WHERE target_id=? AND status='pending'", postTask)
		must(changed.Error)
		if changed.RowsAffected != 1 {
			t.Fatal("停止期间任务未持久保存")
		}
		// 两个正式进程竞争同一持久工作，不能重复公开或产生重复回执。
		whispers = start(workerBin, "-config", workerPath, "-environment", workerEnv, "-kind", "whispers")
		duplicate := start(workerBin, "-config", workerPath, "-environment", workerEnv, "-kind", "whispers")
		wait("重启竞争完成", func() bool {
			var n int64
			return root.Model(&entity.WhisperPost{}).Where("id=? AND published_content_version IS NOT NULL", post.ID).Count(&n).Error == nil && n == 1
		}, whispers, duplicate)
		duplicate.stop(t)
	}
	wait("生产树洞公开", func() bool {
		var n int64
		return root.Model(&entity.WhisperPost{}).Where("id=? AND published_content_version IS NOT NULL", post.ID).Count(&n).Error == nil && n == 1
	}, whispers)
	var public whisper.PublicPost
	request("GET", "/whispers/posts/"+post.ID, "", nil, 200, &public)
	var hug whisper.HugResult
	request("PUT", "/whispers/posts/"+post.ID+"/hugs", peerToken, whisper.HugInput{ExpectedPostVersion: public.Version, Active: true}, 200, &hug)
	if hug.HugCount != 1 || !hug.MyHug.Active {
		t.Fatal("抱抱未保存权威状态")
	}
	request("PUT", "/whispers/posts/"+post.ID+"/hugs", peerToken, whisper.HugInput{ExpectedPostVersion: public.Version, ExpectedHugVersion: hug.MyHug.Version, Active: false}, 200, &hug)
	if hug.HugCount != 0 || hug.MyHug.Active {
		t.Fatal("抱抱取消未生效")
	}
	var comment whisper.OwnerComment
	request("POST", "/whispers/posts/"+post.ID+"/comments", peerToken, whisper.CommentInput{ContentInput: whisper.ContentInput{Body: "隔离暖评", Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedPostVersion: public.Version, ExpectedCommentsEpoch: public.CommentsEpoch}, 202, &comment)
	wait("暖评审核", func() bool {
		var n int64
		return root.Model(&entity.WhisperComment{}).Where("id=? AND review_state='approved'", comment.ID).Count(&n).Error == nil && n == 1
	}, whispers)
	var capsule whisper.Capsule
	request("POST", "/me/whispers/capsules", token, whisper.CapsuleInput{Body: "本人胶囊私密原文", UnlockAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), SafetyNoticeAccepted: true}, 202, &capsule)
	request("GET", "/me/whispers/capsules/"+capsule.ID, token, nil, 200, &capsule)
	if capsule.Body != nil {
		t.Fatal("胶囊提前返回正文")
	}
	request("GET", "/me/whispers/capsules/"+capsule.ID, peerToken, nil, 404, nil)
	wait("胶囊审核", func() bool {
		var n int64
		return root.Model(&entity.WhisperCapsule{}).Where("id=? AND review_state='approved'", capsule.ID).Count(&n).Error == nil && n == 1
	}, whispers)
	must(root.Exec("UPDATE public.whisper_capsules SET created_at=clock_timestamp()-interval '2 minutes',unlock_at=date_trunc('second',clock_timestamp())-interval '1 second' WHERE id=?", capsule.ID).Error)
	must(root.Exec("UPDATE public.work_items SET next_at=clock_timestamp() WHERE target_id=? AND kind='whisper_capsule_unlock'", capsule.ID).Error)
	must(root.Exec("UPDATE public.outbox_events SET next_at=clock_timestamp() WHERE aggregate_id=? AND event_type='whisper.capsule.unlock.requested'", capsule.ID).Error)
	wait("胶囊解锁", func() bool {
		var n int64
		return root.Model(&entity.WhisperNotificationRef{}).Where("source_id=?", capsule.ID).Count(&n).Error == nil && n == 1
	}, whispers)
	request("GET", "/me/whispers/capsules/"+capsule.ID, token, nil, 200, &capsule)
	if capsule.Body == nil || *capsule.Body != "本人胶囊私密原文" {
		t.Fatal("解锁内容错误")
	}
	if workerEnv == "production" && !databaseTasks {
		for _, event := range []string{"student_card.scan_requested", "wish.review_requested", "whisper.review.requested", "whisper.capsule.unlock.requested"} {
			wait("鉴权消息发送回执", func() bool {
				var n int64
				return root.Model(&entity.OutboxEvent{}).Where("event_type=? AND sent_at IS NOT NULL", event).Count(&n).Error == nil && n > 0
			}, card, content, whispers)
		}
	}
	if databaseTasks {
		var sent, receipts int64
		must(root.Model(&entity.OutboxEvent{}).Where("sent_at IS NOT NULL").Count(&sent).Error)
		must(root.Model(&entity.ConsumerReceipt{}).Where("operation_id=?", postTask).Count(&receipts).Error)
		if sent != 0 || receipts != 1 {
			t.Fatalf("数据库调度不得伪造MQ回执或重复消费: sent=%d receipts=%d", sent, receipts)
		}
	}
	outage.Store(true)
	if health("/readyz") != 503 || health("/healthz") != 200 {
		t.Fatal("供应商故障未区分就绪与存活")
	}
	outage.Store(false)
	var account entity.Account
	must(root.Where("id=?", actor).Take(&account).Error)
	var accepted privacy.Accepted
	request("POST", "/me/deletion-requests", token, privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: account.Version}, 202, &accepted)
	request("GET", "/me", token, nil, 401, nil)
	server.stop(t)
	card.stop(t)
	content.stop(t)
	whispers.stop(t)
	oneShot(serverBin, false, "-config", appPath, "-check")
	eraseFailure.Store(true)
	oneShot(workerBin, false, "-config", workerPath, "-environment", workerEnv, "-kind", "account", "-recover")
	var remaining int64
	must(root.Model(&entity.DeletionJob{}).Where("phase<>'completed' AND scope='account'").Count(&remaining).Error)
	if remaining == 0 {
		t.Fatal("外部删除失败被误报完成")
	}
	eraseFailure.Store(false)
	must(root.Exec("UPDATE public.deletion_jobs SET next_at=clock_timestamp() WHERE scope='account'").Error)
	oneShot(workerBin, true, "-config", workerPath, "-environment", workerEnv, "-kind", "account", "-recover")
	oneShot(serverBin, true, "-config", appPath, "-check")
	must(root.Create(&entity.Account{ID: actor, Nickname: "模拟恢复出的已注销主体"}).Error)
	oneShot(serverBin, false, "-config", appPath, "-check")
	oneShot(workerBin, true, "-config", workerPath, "-environment", workerEnv, "-kind", "account", "-recover")
	oneShot(serverBin, true, "-config", appPath, "-check")
	if eraseCalls.Load() == 0 {
		t.Fatal("外部清理未调用")
	}
	bad := apiCfg
	bad.Runtime.APIDSN = businessDSN
	must(os.WriteFile(apiPath, mustJSON(t, bad), 0600))
	oneShot(serverBin, false, "-config", appPath, "-check")
	var latest int64
	must(root.Raw("SELECT max(version) FROM public.schema_migrations").Scan(&latest).Error)
	if latest != 29 {
		t.Fatal("运行入口改变迁移版本", latest)
	}
	t.Logf("完整生产API、五类worker诊断、认证上传/扫描/审核、普通心愿审核、悄悄鲤/暖评/抱抱/胶囊、恢复失败关闭通过；worker环境=%s，外部依赖为隔离TLS合成", workerEnv)
}

func loadMQTemplate(kind string, c *workrunner.Config) error {
	return config.LoadCardWorkerMQ("../../configs/examples/production-"+kind+"-mq.yaml", c)
}
