package whisper_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

type reviewSQLLogger struct {
	logger.Interface
	t *testing.T
}

func (l reviewSQLLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		l.t.Logf("数据库错误：%v", err)
	}
}

type reviewNotifier struct{}

func (reviewNotifier) NotifyCrisis(ctx context.Context, deliveryID, caseID string) error {
	return ctx.Err()
}

type reviewAnalyzer struct {
	hook           func()
	privateCalls   int
	privateFailure error
	privateCrisis  bool
}

func (a *reviewAnalyzer) AnalyzeWhisper(ctx context.Context, kind, body string, i whisper.IdentityInput) (moderation.WhisperAssessment, error) {
	if a.hook != nil {
		a.hook()
	}
	return moderation.WhisperAssessment{Body: strings.ReplaceAll(body, "合成人名", "[已脱敏]"), Identity: i, DetectorVersion: "synthetic-public", OrdinaryHit: strings.Contains(body, "普通命中")}, ctx.Err()
}
func (a *reviewAnalyzer) ClassifyWhisperCapsule(ctx context.Context, body string) (moderation.MessageAssessment, error) {
	a.privateCalls++
	if a.hook != nil {
		a.hook()
	}
	if a.privateFailure != nil {
		return moderation.MessageAssessment{}, a.privateFailure
	}
	return moderation.MessageAssessment{DetectorVersion: "synthetic-private", CrisisHit: a.privateCrisis, OrdinaryHit: strings.Contains(body, "普通命中")}, ctx.Err()
}

// TestPostgresWhisperModeration 验证B04的真实角色、消费、治理和双版本栅栏；外部供应商仍为合成。
func TestPostgresWhisperModeration(t *testing.T) {
	dsn := os.Getenv("APP_WHISPER_TEXT_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离 PostgreSQL；make integration 覆盖此用例")
	}
	open := func(value string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: reviewSQLLogger{Interface: logger.Default.LogMode(logger.Silent), t: t}})
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
	name := "whisper_review_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	cfg := whisper.Config{Enabled: true, Command: command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: 24 * time.Hour, MaxAttempts: 3}, ReceiptKey: bytes.Repeat([]byte{2}, 32), CursorKey: bytes.Repeat([]byte{3}, 32)}
	service, e := whisper.NewService(api, log, codec, catalog, admission, moderation.WhisperReviewSink{Logger: log}, cfg)
	must(e)
	adminDB := open(testDSN + " options='-c role=liyuanci_admin_api'")
	workerDB := open(testDSN + " options='-c role=liyuanci_domain_worker'")
	crisisDB := open(testDSN + " options='-c role=liyuanci_crisis_api'")
	reviews, e := moderation.New(adminDB, log, codec, moderation.Config{Command: cfg.Command, CursorKey: bytes.Repeat([]byte{8}, 32), MaxEntries: 100})
	must(e)
	crises, e := moderation.New(crisisDB, log, codec, moderation.Config{Command: cfg.Command, CursorKey: bytes.Repeat([]byte{8}, 32), MaxEntries: 100})
	must(e)
	student, e := moderation.New(api, log, codec, moderation.Config{Command: cfg.Command, CursorKey: bytes.Repeat([]byte{8}, 32), MaxEntries: 100})
	must(e)
	adminID, adminToken := uuid.NewString(), uuid.NewString()
	hash := sha256.Sum256([]byte(adminToken))
	credential := uuid.NewString()
	must(admin.Create(&entity.AdminAccount{ID: adminID, CredentialRef: &credential}).Error)
	must(admin.Create(&entity.AdminRoleBinding{AdminID: adminID, Role: "super_admin"}).Error)
	must(admin.Exec("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), adminID, hash[:]).Error)
	for _, typ := range []string{"crisis", "school", "ordinary_violation"} {
		id := uuid.NewString()
		must(admin.Create(&entity.WordlistVersion{ID: id, Type: typ, Version: 1, Status: "published"}).Error)
		must(admin.Exec("INSERT INTO public.wordlist_heads(kind,version,effective_id) VALUES (?,1,?) ON CONFLICT(kind) DO UPDATE SET effective_id=EXCLUDED.effective_id", typ, id).Error)
		if typ == "ordinary_violation" {
			must(admin.Create(&entity.WordlistEntry{ID: uuid.NewString(), VersionID: id, Entry: "普通命中", Action: "ordinary_violation"}).Error)
		}
		if typ == "crisis" {
			must(admin.Create(&entity.WordlistEntry{ID: uuid.NewString(), VersionID: id, Entry: "危机命中", Action: "isolate"}).Error)
		}
	}

	// 独立逻辑日志库验证持久删除受理；同一实例不代表物理故障域隔离。
	journalName := name + "_journal"
	must(parent.Exec("CREATE DATABASE " + journalName).Error)
	journalDSN := strings.Replace(dsn, needle, "dbname="+journalName+" ", 1)
	journalRoot := open(journalDSN)
	must(migration.RunJournal(ctx, journalRoot, log, "up"))
	must(migration.RunJournal(ctx, journalRoot, log, "permissions"))
	journal, e := privacy.NewJournal(ctx, open(journalDSN+" options='-c role=liyuanci_recovery_writer'"), open(journalDSN+" options='-c role=liyuanci_recovery_reader'"), codec, log, 5*time.Second)
	must(e)
	deletion, e := privacy.New(open(testDSN+" options='-c role=liyuanci_evidence_delete_api'"), journal, codec, nil, log, privacy.Config{Command: cfg.Command, Key: bytes.Repeat([]byte{12}, 32), ReceiptTTL: 24 * time.Hour, RetryDelay: time.Second})
	must(e)
	router := httpapi.New(log, nil)
	must(httpapi.RegisterWhisperGovernance(router, reviews, crises, student, deletion))
	samples := []map[string]any{}

	legacySafety, e := safety.New(adminDB, log, codec, safety.Config{Command: cfg.Command, Key: bytes.Repeat([]byte{10}, 32), MaxTextBytes: 2000})
	must(e)
	studentSafety, e := safety.New(api, log, codec, safety.Config{Command: cfg.Command, Key: bytes.Repeat([]byte{10}, 32), MaxTextBytes: 2000})
	must(e)
	careService, e := chat.New(api, log, codec, chat.Config{Command: cfg.Command, Key: bytes.Repeat([]byte{13}, 32), MaxBodyBytes: 2000})
	must(e)
	roleToken := func(role string) string {
		id, tok, credential := uuid.NewString(), uuid.NewString(), uuid.NewString()
		must(admin.Create(&entity.AdminAccount{ID: id, CredentialRef: &credential}).Error)
		must(admin.Create(&entity.AdminRoleBinding{AdminID: id, Role: role}).Error)
		hash := sha256.Sum256([]byte(tok))
		must(admin.Exec("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), id, hash[:]).Error)
		return tok
	}
	reviewToken, crisisToken := roleToken("review_operator"), roleToken("crisis_operator")
	request := func(op, method, path, bearer string, body any, want int, keys ...string) json.RawMessage {
		t.Helper()
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		requestKey := uuid.NewString()
		if len(keys) > 0 {
			requestKey = keys[0]
		}
		req.Header.Set("Idempotency-Key", requestKey)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s 状态=%d 正文=%s", method, path, rec.Code, rec.Body.String())
		}
		var raw struct {
			Data json.RawMessage `json:"data"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &raw))
		samples = append(samples, map[string]any{"operation_id": op, "status": want, "response": json.RawMessage(append([]byte(nil), rec.Body.Bytes()...))})
		return raw.Data
	}
	t.Cleanup(func() {
		if path := os.Getenv("APP_WHISPER_GOVERNANCE_WIRE_OUTPUT"); path != "" {
			b, _ := json.MarshalIndent(samples, "", "  ")
			if e := os.WriteFile(path, b, 0600); e != nil {
				t.Error(e)
			}
		}
	})
	// 已认证来源只有学号密文，不复制学生卡或实名资料。
	env, e := codec.Encrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: actor, Version: 1}, []byte("SYNTHETIC_STUDENT"))
	must(e)
	identityRaw, _ := json.Marshal(env)
	must(admin.Create(&entity.IdentityLookupKey{Purpose: "student_number", CurrentRef: "synthetic-lookup", Version: 1}).Error)
	must(admin.Create(&entity.IdentitySecret{AccountID: actor, Purpose: "student_number", ContentVersion: 1, Envelope: identityRaw}).Error)
	analyzer := &reviewAnalyzer{}
	worker, e := moderation.NewWhisperWorker(workerDB, log, codec, analyzer, analyzer, moderation.WorkerConfig{AnalysisTimeout: time.Second, CrisisNotifier: reviewNotifier{}, Consumer: durable.ConsumerConfig{Timeout: 5 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}, Care: &moderation.CareResources{Version: "synthetic", Source: "合成资料", VerifiedAt: "2026-10-28", Hotline24Hours: "合成热线", Appointment: "合成预约", Notice: "合成资料非生产"}})
	must(e)
	create := func(body string) whisper.OwnerPost {
		t.Helper()
		v, e := service.CreatePost(ctx, token, uuid.NewString(), "", whisper.ContentInput{Body: body, Identity: whisper.IdentityInput{Mode: "anonymous"}})
		must(e)
		v, e = service.ChangePost(ctx, token, uuid.NewString(), "", v.ID, "submit", whisper.PostInput{ExpectedVersion: v.Version})
		must(e)
		return v
	}
	event := func(id string) durable.Event {
		t.Helper()
		var ev durable.Event
		must(admin.Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.entity_id=? ORDER BY t.created_at DESC LIMIT 1`, id).Scan(&ev).Error)
		return ev
	}
	task := func(id string) entity.ModerationTask {
		t.Helper()
		var v entity.ModerationTask
		must(admin.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.entity_id=?", id).Order("t.created_at DESC").Take(&v).Error)
		return v
	}
	t.Run("自动发布及持久重放", func(t *testing.T) {
		v := create("合成人名 的公开文字")
		ev := event(v.ID)
		result, e := worker.Consume(ctx, ev)
		must(e)
		if !result.Applied {
			t.Fatal(result)
		}
		p, e := service.GetPost(ctx, "", "", v.ID, false)
		must(e)
		if p.(whisper.PublicPost).Body != "合成人名 的公开文字" {
			t.Fatal("未命中未保留原文候选")
		}
		result, e = worker.Consume(ctx, ev)
		must(e)
		if !result.Replayed {
			t.Fatal(result)
		}
	})
	t.Run("人工双版本及治理重新送审", func(t *testing.T) {
		v := create("普通命中 合成人名")
		_, e := worker.Consume(ctx, event(v.ID))
		must(e)
		row := task(v.ID)
		detail, e := reviews.WhisperReview(ctx, adminToken, "", row.ID)
		must(e)
		if detail.Body != "普通命中 合成人名" {
			t.Fatal(detail)
		}
		_, e = reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", row.ID, moderation.WhisperReviewCommand{ExpectedVersion: row.Version, ExpectedContentVersion: 2, Decision: "approved"})
		if !errors.Is(e, command.ErrVersionConflict) {
			t.Fatal(e)
		}
		out, e := reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", row.ID, moderation.WhisperReviewCommand{ExpectedVersion: row.Version, ExpectedContentVersion: 1, Decision: "approved"})
		must(e)
		if out.BusinessOutcome != "published" {
			t.Fatal(out)
		}
		control, e := reviews.WhisperContent(ctx, adminToken, "", "whisper_post", v.ID)
		must(e)
		control, e = reviews.SetWhisperRestriction(ctx, adminToken, uuid.NewString(), "", "whisper_post", v.ID, moderation.WhisperRestrictionCommand{ExpectedVersion: control.Version, Restricted: true, Reason: "合成违规"})
		must(e)
		if !control.Restricted {
			t.Fatal(control)
		}
		_, e = service.GetPost(ctx, "", "", v.ID, false)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		control, e = reviews.SetWhisperRestriction(ctx, adminToken, uuid.NewString(), "", "whisper_post", v.ID, moderation.WhisperRestrictionCommand{ExpectedVersion: control.Version, Restricted: false, Reason: "合成解除"})
		must(e)
		if control.State != "scanning" {
			t.Fatal(control)
		}
		_, e = worker.Consume(ctx, event(v.ID))
		must(e)
	})
	t.Run("分析期间撤回不放行", func(t *testing.T) {
		v := create("普通命中 迟到分析")
		analyzer.hook = func() {
			_, e := service.ChangePost(ctx, token, uuid.NewString(), "", v.ID, "withdraw", whisper.PostInput{ExpectedVersion: v.Version})
			must(e)
		}
		defer func() { analyzer.hook = nil }()
		result, e := worker.Consume(ctx, event(v.ID))
		must(e)
		if !result.Cancelled {
			t.Fatal(result)
		}
		_, e = service.GetPost(ctx, "", "", v.ID, false)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
	})
	t.Run("举报确认处罚及旧接口隔离", func(t *testing.T) {
		v := create("可举报的文字")
		_, e := worker.Consume(ctx, event(v.ID))
		must(e)
		must(admin.Create(&entity.ReportType{Code: "synthetic", Label: "合成类型", Published: true}).Error)
		ack, e := student.CreateWhisperReport(ctx, otherToken, uuid.NewString(), "", moderation.WhisperReportInput{Source: moderation.WhisperRef{Kind: "whisper_post", ID: v.ID, ContentVersion: 1}, TypeCode: "synthetic", Description: "合成说明"})
		must(e)
		detail, e := reviews.WhisperReport(ctx, adminToken, "", ack.ID)
		must(e)
		if detail.Description == nil || *detail.Description != "合成说明" {
			t.Fatal(detail)
		}
		report, e := reviews.DecideWhisperReport(ctx, adminToken, uuid.NewString(), "", ack.ID, moderation.WhisperReportDecision{ExpectedVersion: 1, Decision: "confirmed", Reason: "合成确认"})
		must(e)
		control, e := reviews.WhisperContent(ctx, adminToken, "", "whisper_post", v.ID)
		must(e)
		penalty, e := reviews.NewWhisperPenalty(ctx, adminToken, uuid.NewString(), "", "whisper_post", v.ID, moderation.WhisperPenaltyCommand{ExpectedVersion: control.Version, ReportID: ack.ID, ExpectedReportVersion: report.Version, Level: "minor", Reason: "合成警告"})
		must(e)
		if penalty.Kind != "warning" {
			t.Fatal(penalty)
		}
		// 树洞处罚复用本人警告和申诉，结果不包含匿名来源的作者映射。
		warnings, e := studentSafety.List(ctx, token, "", "warnings", safety.Query{Limit: 20})
		must(e)
		if len(warnings.Items) != 1 {
			t.Fatal(warnings)
		}
		_, e = studentSafety.Execute(ctx, token, uuid.NewString(), "", "appeal", "", safety.Input{PenaltyID: penalty.ID, Body: "合成申诉"})
		must(e)
	})

	t.Run("暖评父上下文与胶囊私密分类适配", func(t *testing.T) {
		parent := create("合规父帖")
		_, e := worker.Consume(ctx, event(parent.ID))
		must(e)
		encrypt := func(purpose, id string, value any) json.RawMessage {
			b, _ := json.Marshal(value)
			env, e := codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: 1}, b)
			must(e)
			raw, _ := json.Marshal(env)
			return raw
		}
		seed := func(kind, body string) (string, durable.Event) {
			id, registryID, taskID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			zero := int64(0)
			must(admin.Transaction(func(db *gorm.DB) error {
				if e := db.Create(&entity.ContentRegistry{ID: registryID, Kind: kind, EntityID: id, AuthorID: other, ContentVersion: 1, Status: "pending", SchoolDetectionVersion: "pending"}).Error; e != nil {
					return e
				}
				if e := db.Create(&entity.ModerationTask{ID: taskID, ContentID: registryID, ContentVersion: 1, SubjectEpoch: 1, RestrictionEpoch: &zero, Status: "pending"}).Error; e != nil {
					return e
				}
				if kind == "whisper_capsule" {
					if e := db.Create(&entity.WhisperCapsule{ID: id, OwnerID: other, ContentRegistryID: registryID, BodyCipher: encrypt("whisper_capsule_body", id, body), UnlockAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second), ReviewRound: 1, ReviewTaskID: &taskID}).Error; e != nil {
						return e
					}
				} else {
					if e := db.Create(&entity.WhisperComment{ID: id, AuthorID: other, PostID: &parent.ID, ParentContentVersion: 1, CommentsEpoch: 1, ReviewRound: 1}).Error; e != nil {
						return e
					}
					if e := db.Create(&entity.WhisperCommentVersion{CommentID: id, ContentVersion: 1, ContentRegistryID: registryID, BodyCipher: encrypt("whisper_comment_body", id, body), IdentityCipher: encrypt("whisper_comment_identity", id, whisper.IdentityInput{Mode: "anonymous"}), IdentityMode: "anonymous", ReviewTaskID: &taskID}).Error; e != nil {
						return e
					}
				}
				return (moderation.WhisperReviewSink{Logger: log}).Append(ctx, db, whisper.ReviewSubmission{TaskID: taskID, ContentID: registryID, Kind: kind, ContentVersion: 1, Round: 1})
			}))
			return id, event(id)
		}
		capsule, ev := seed("whisper_capsule", "普通命中 合成人名的私密胶囊")
		var before string
		must(admin.Raw("SELECT body_cipher::text FROM public.whisper_capsules WHERE id=?", capsule).Scan(&before).Error)
		_, e = worker.Consume(ctx, ev)
		must(e)
		if analyzer.privateCalls != 1 {
			t.Fatal("胶囊未走独立分类")
		}
		row := task(capsule)
		detail, e := reviews.WhisperReview(ctx, adminToken, "", row.ID)
		must(e)
		if detail.Body != "普通命中 合成人名的私密胶囊" {
			t.Fatal(detail)
		}
		_, e = reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", row.ID, moderation.WhisperReviewCommand{ExpectedVersion: row.Version, ExpectedContentVersion: 1, Decision: "approved"})
		must(e)
		var after string
		must(admin.Raw("SELECT body_cipher::text FROM public.whisper_capsules WHERE id=?", capsule).Scan(&after).Error)
		if before != after {
			t.Fatal("私密胶囊原文被公开脱敏替换")
		}
		control, e := reviews.WhisperContent(ctx, adminToken, "", "whisper_capsule", capsule)
		must(e)
		if control.State != "locked" {
			t.Fatal(control)
		}
		must(admin.Exec("UPDATE public.whisper_capsules SET unlock_at=date_trunc('second',clock_timestamp())-interval '1 second' WHERE id=?", capsule).Error)
		control, e = reviews.WhisperContent(ctx, adminToken, "", "whisper_capsule", capsule)
		must(e)
		if control.State != "unlocked" {
			t.Fatal(control)
		}
		comment, ev := seed("whisper_comment", "合成人名的暖评")
		_, e = worker.Consume(ctx, ev)
		must(e)
		var body string
		must(admin.Raw("SELECT public_body FROM public.whisper_comment_versions WHERE comment_id=?", comment).Scan(&body).Error)
		if body != "合成人名的暖评" {
			t.Fatal(body)
		}
		// 暖评不要求学生认证；举报公开暖评也不能额外添加作者认证门槛。
		must(admin.Exec("UPDATE public.accounts SET verification='unverified' WHERE id=?", other).Error)
		_, e = student.CreateWhisperReport(ctx, token, uuid.NewString(), "", moderation.WhisperReportInput{Source: moderation.WhisperRef{Kind: "whisper_comment", ID: comment, ContentVersion: 1}, TypeCode: "synthetic"})
		must(e)
		must(admin.Exec("UPDATE public.accounts SET verification='verified' WHERE id=?", other).Error)
		comment, ev = seed("whisper_comment", "普通命中 迟到暖评")
		analyzer.hook = func() {
			current, e := service.GetPost(ctx, token, "", parent.ID, true)
			must(e)
			v := current.(whisper.OwnerPost)
			_, e = service.ChangePost(ctx, token, uuid.NewString(), "", parent.ID, "withdraw", whisper.PostInput{ExpectedVersion: v.Version})
			must(e)
		}
		result, e := worker.Consume(ctx, ev)
		analyzer.hook = nil
		must(e)
		if !result.Cancelled || task(comment).Status != "obsolete" && task(comment).Status != "cancelled" {
			t.Fatal("父帖撤回后仍放行暖评")
		}
	})
	t.Run("后台HTTP契约及证据删除持久受理", func(t *testing.T) {
		a := create("普通命中 待人工升级")
		_, e := worker.Consume(ctx, event(a.ID))
		must(e)
		ta := task(a.ID)
		request("whisperAdminReviewsList", "GET", "/admin/whispers/reviews", adminToken, nil, 200)
		request("whisperAdminReviewGet", "GET", "/admin/whispers/reviews/"+ta.ID, adminToken, nil, 200)
		b := create("普通命中 待人工决定")
		_, e = worker.Consume(ctx, event(b.ID))
		must(e)
		tb := task(b.ID)
		request("whisperAdminReviewDecide", "POST", "/admin/whispers/reviews/"+tb.ID+"/decision", adminToken, map[string]any{"expected_version": tb.Version, "expected_content_version": 1, "decision": "approved"}, 200)
		controlRaw := request("whisperAdminContentGet", "GET", "/admin/whispers/contents/whisper_post/"+b.ID, adminToken, nil, 200)
		var control moderation.WhisperControl
		must(json.Unmarshal(controlRaw, &control))
		request("whisperAdminRestrictionSet", "PUT", "/admin/whispers/contents/whisper_post/"+b.ID+"/restriction", adminToken, map[string]any{"expected_version": control.Version, "restricted": true, "reason": "合成限制"}, 200)
		// 普通任务由超管同时具有两个域权限后升格。
		crisisRaw := request("whisperAdminReviewEscalate", "POST", "/admin/whispers/reviews/"+ta.ID+"/escalate", adminToken, map[string]any{"expected_version": ta.Version, "reason": "合成危机判断"}, 200)
		var c moderation.WhisperCrisisSummary
		must(json.Unmarshal(crisisRaw, &c))
		request("whisperAdminCrisesList", "GET", "/admin/whispers/crises", adminToken, nil, 200)
		detailRaw := request("whisperAdminCrisisGet", "GET", "/admin/whispers/crises/"+c.ID, adminToken, nil, 200)
		var cd moderation.WhisperCrisisDetail
		must(json.Unmarshal(detailRaw, &cd))
		if cd.StudentNumber == nil || *cd.StudentNumber != "SYNTHETIC_STUDENT" {
			t.Fatal(cd)
		}
		request("whisperAdminCrisisAct", "POST", "/admin/whispers/crises/"+c.ID+"/actions", adminToken, map[string]any{"expected_version": c.Version, "action": "take_over", "reason": "合成接手"}, 200)
		request("whisperAdminCrisisFalsePositive", "POST", "/admin/whispers/crises/"+c.ID+"/false-positive", adminToken, map[string]any{"expected_version": c.Version + 1, "reason": "合成误报"}, 200)
		public := create("公开举报来源")
		_, e = worker.Consume(ctx, event(public.ID))
		must(e)
		ackRaw := request("whisperReportCreate", "POST", "/whispers/reports", otherToken, map[string]any{"source": moderation.WhisperRef{Kind: "whisper_post", ID: public.ID, ContentVersion: 1}, "type_code": "synthetic"}, 202)
		var ack moderation.WhisperReportAck
		must(json.Unmarshal(ackRaw, &ack))
		request("whisperAdminReportsList", "GET", "/admin/whispers/reports", adminToken, nil, 200)
		request("whisperAdminReportGet", "GET", "/admin/whispers/reports/"+ack.ID, adminToken, nil, 200)
		request("whisperAdminReportDecide", "POST", "/admin/whispers/reports/"+ack.ID+"/decision", adminToken, map[string]any{"expected_version": 1, "decision": "confirmed", "reason": "合成确认"}, 200)
		control, e = reviews.WhisperContent(ctx, adminToken, "", "whisper_post", public.ID)
		must(e)
		request("whisperAdminPenaltyCreate", "POST", "/admin/whispers/contents/whisper_post/"+public.ID+"/penalties", adminToken, map[string]any{"expected_version": control.Version, "report_id": ack.ID, "expected_report_version": 2, "level": "minor", "reason": "合成警告"}, 200)
		public = create("另一公开举报来源")
		_, e = worker.Consume(ctx, event(public.ID))
		must(e)
		ack, e = student.CreateWhisperReport(ctx, otherToken, uuid.NewString(), "", moderation.WhisperReportInput{Source: moderation.WhisperRef{Kind: "whisper_post", ID: public.ID, ContentVersion: 1}, TypeCode: "synthetic"})
		must(e)
		escalationKey := uuid.NewString()
		crisisRaw = request("whisperAdminReportEscalate", "POST", "/admin/whispers/reports/"+ack.ID+"/escalate", adminToken, map[string]any{"expected_version": 1, "reason": "合成升级"}, 200, escalationKey)
		request("whisperAdminReportEscalate", "POST", "/admin/whispers/reports/"+ack.ID+"/escalate", adminToken, map[string]any{"expected_version": 1, "reason": "合成升级"}, 200, escalationKey)
		must(json.Unmarshal(crisisRaw, &c))
		detailRaw = request("whisperAdminCrisisGet", "GET", "/admin/whispers/crises/"+c.ID, adminToken, nil, 200)
		must(json.Unmarshal(detailRaw, &cd))

		request("whisperAdminReviewGet", "GET", "/admin/whispers/reviews/"+tb.ID, crisisToken, nil, 403)
		request("whisperAdminCrisisGet", "GET", "/admin/whispers/crises/"+c.ID, reviewToken, nil, 403)
		request("whisperAdminReviewEscalate", "POST", "/admin/whispers/reviews/"+tb.ID+"/escalate", reviewToken, map[string]any{"expected_version": tb.Version, "reason": "越权尝试"}, 403)
		oldReviews, e := reviews.Cases(ctx, adminToken, "", "", 20)
		must(e)
		if len(oldReviews.Items) != 0 {
			t.Fatal("旧审核列表泄漏树洞")
		}
		oldCrises, e := crises.CrisisCases(ctx, adminToken, "", "", 20)
		must(e)
		if len(oldCrises.Items) != 0 {
			t.Fatal("旧危机列表泄漏树洞")
		}
		oldReports, e := legacySafety.List(ctx, adminToken, "", "reports", safety.Query{Limit: 20})
		must(e)
		if len(oldReports.Items) != 0 {
			t.Fatal("旧举报列表泄漏树洞")
		}
		_, e = legacySafety.Detail(ctx, adminToken, "", "report_detail", ack.ID)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		_, e = crises.CrisisEvidence(ctx, adminToken, "", cd.EvidenceScopeID)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		_, e = deletion.DeleteEvidence(ctx, adminToken, uuid.NewString(), "", cd.EvidenceScopeID, privacy.EvidenceInput{ExpectedVersion: 1, Reason: "旧入口尝试"})
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		scopeRaw := request("whisperAdminEvidenceGet", "GET", "/admin/whispers/evidence/"+cd.EvidenceScopeID, adminToken, nil, 200)
		var scope moderation.WhisperEvidenceScope
		must(json.Unmarshal(scopeRaw, &scope))
		jobRaw := request("whisperAdminEvidenceDelete", "POST", "/admin/whispers/evidence/"+scope.ID+"/deletion-requests", adminToken, map[string]any{"expected_version": scope.Version, "confirmation": "DELETE_EVIDENCE_SCOPE", "reason": "合成删除证据"}, 202)
		var job struct {
			ID string `json:"id"`
		}
		must(json.Unmarshal(jobRaw, &job))

		_, e = deletion.EvidenceStatus(ctx, adminToken, "", job.ID)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal("旧删除回执泄漏树洞", e)
		}
		request("whisperAdminDeletionGet", "GET", "/admin/whispers/deletions/"+job.ID, adminToken, nil, 200)
		// 严格字段校验发生在命令之前，错误不允许宽松解码为成功。
		for _, body := range []string{`{"expected_version":1,"restricted":null,"reason":"x"}`, `{"expected_version":1,"restricted":true,"Reason":"x"}`, `{"expected_version":1,"expected_version":1,"restricted":true,"reason":"x"}`} {
			req := httptest.NewRequest("PUT", "/api/v1/admin/whispers/contents/whisper_post/"+b.ID+"/restriction", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+adminToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", uuid.NewString())
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != 400 {
				t.Fatal(rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("昵称候选激活及历史快照撤销", func(t *testing.T) {
		state, e := service.GetNickname(ctx, otherToken, "")
		must(e)
		_, e = service.SetNickname(ctx, otherToken, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: state.Version, Label: "合成昵称"})
		must(e)
		result, e := worker.ProcessNext(ctx)
		must(e)
		_ = result
		// 其他待审工作可能在前，按昵称精确任务消费，不依赖数据库队列顺序。
		var n entity.WhisperNicknameVersion
		must(admin.Where("account_id=? AND nickname_version=1", other).Take(&n).Error)
		ev := event(n.ContentRegistryID)
		_, e = worker.Consume(ctx, ev)
		must(e)
		first := int64(1)
		named, e := service.CreatePost(ctx, otherToken, uuid.NewString(), "", whisper.ContentInput{Body: "具名合成正文", Identity: whisper.IdentityInput{Mode: "named", NicknameVersion: &first}})
		must(e)
		_, e = service.ChangePost(ctx, otherToken, uuid.NewString(), "", named.ID, "submit", whisper.PostInput{ExpectedVersion: named.Version})
		must(e)
		_, e = worker.Consume(ctx, event(named.ID))
		must(e)
		state, e = service.GetNickname(ctx, otherToken, "")
		must(e)
		_, e = service.SetNickname(ctx, otherToken, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: state.Version, Label: "普通命中新昵称"})
		must(e)
		var next entity.WhisperNicknameVersion
		must(admin.Where("account_id=? AND nickname_version=2", other).Take(&next).Error)
		_, e = worker.Consume(ctx, event(next.ContentRegistryID))
		must(e)
		row := task(next.ContentRegistryID)
		out, e := reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", row.ID, moderation.WhisperReviewCommand{ExpectedVersion: row.Version, ExpectedContentVersion: 2, Decision: "approved"})
		must(e)
		if out.BusinessOutcome != "nickname_activated" {
			t.Fatal(out)
		}
		_, e = service.GetPost(ctx, "", "", named.ID, false)
		must(e)
		control, e := reviews.WhisperContent(ctx, adminToken, "", "whisper_nickname", n.ContentRegistryID)
		must(e)
		_, e = reviews.SetWhisperRestriction(ctx, adminToken, uuid.NewString(), "", "whisper_nickname", n.ContentRegistryID, moderation.WhisperRestrictionCommand{ExpectedVersion: control.Version, Restricted: true, Reason: "合成撤销历史昵称"})
		must(e)
		_, e = service.GetPost(ctx, "", "", named.ID, false)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal("昵称撤销未隐藏历史内容", e)
		}
	})
	t.Run("未实名昵称危机无学号与误报复核", func(t *testing.T) {
		must(admin.Exec("UPDATE public.accounts SET verification='unverified' WHERE id=?", other).Error)
		state, e := service.GetNickname(ctx, otherToken, "")
		must(e)
		n, e := service.SetNickname(ctx, otherToken, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: state.Version, Label: "危机命中"})
		must(e)
		_ = n
		var ev durable.Event
		must(admin.Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.kind='whisper_nickname' AND c.author_id=? ORDER BY t.created_at DESC LIMIT 1`, other).Scan(&ev).Error)
		_, e = worker.Consume(ctx, ev)
		must(e)
		page, e := crises.WhisperCrises(ctx, adminToken, "", "", 20)
		must(e)
		var c moderation.WhisperCrisisSummary
		for _, item := range page.Items {
			if item.Source.Kind == "whisper_nickname" {
				c = item
			}
		}
		if c.ID == "" {
			t.Fatal(page)
		}
		detail, e := crises.WhisperCrisis(ctx, adminToken, "", c.ID)
		must(e)
		if detail.IdentityStatus != "not_verified" || detail.StudentNumber != nil || detail.Body != "危机命中" {
			t.Fatal(detail)
		}
		_, e = crises.CrisisCase(ctx, adminToken, "", c.ID)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal("旧接口泄漏树洞危机", e)
		}
		for i := 0; i < 2; i++ {
			_, e = worker.ProcessCrisisDelivery(ctx)
			must(e)
		}
		// 未实名本人仍能读关怀资料，他人不能借卡片标识跨账号读取。
		var cardID string
		must(admin.Raw("SELECT id FROM public.care_cards WHERE recipient_id=?", other).Scan(&cardID).Error)
		_, e = careService.Care(ctx, otherToken, "", cardID)
		must(e)
		_, e = careService.Care(ctx, token, "", cardID)
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		detail, e = crises.ActOnWhisperCrisis(ctx, adminToken, uuid.NewString(), "", c.ID, moderation.CrisisCommand{ExpectedVersion: c.Version, Reason: "合成误报"}, true)
		must(e)
		if detail.Status != "false_positive" {
			t.Fatal(detail)
		}
	})
	t.Run("暖评抱抱通知真实闭环", func(t *testing.T) {
		testWhisperInteractions(t, admin, api, service, worker, analyzer, actor, other, token, otherToken, careService, studentSafety, reviews, adminToken)
	})

	t.Run("本人时空胶囊真实闭环", func(t *testing.T) {
		testWhisperCapsules(t, admin, workerDB, service, worker, analyzer, reviews, crises, other, otherToken, token, adminToken)
	})

	t.Run("匿名兼容与注销恢复真实闭环", func(t *testing.T) {
		testWhisperPrivacy(t, admin, api, open(testDSN+" options='-c role=liyuanci_privacy_maintenance'"), service, worker, codec, journal, cfg, catalog, admission, analyzer)
	})

}
