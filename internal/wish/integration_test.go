package wish

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// TestPostgresCreateDraft 追溯 T014/REQ-003/004/005/018：隔离PG真实事务与运行角色；密钥仅为合成适配器，不做跨端联调。
func TestPostgresCreateDraft(t *testing.T) {
	dsn := os.Getenv("APP_WISH_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PostgreSQL：python3 scripts/check-wish.py")
	}
	ctx := context.Background()
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	open := func(role string) *gorm.DB {
		t.Helper()
		connection := dsn
		if role != "" {
			connection += " options='-c role=" + role + "'"
		}
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: connection, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if err != nil {
			t.Fatal("隔离数据库连接失败")
		}
		pool, _ := db.DB()
		t.Cleanup(func() { pool.Close() })
		return db
	}
	root := open("")
	if err := migration.Run(ctx, root, logger, "up"); err != nil {
		t.Fatal(err)
	}
	if err := migration.ApplyPermissions(ctx, root, logger); err != nil {
		t.Fatal(err)
	}
	api := open("liyuanci_api")
	keys := &testKeys{}
	codec, _ := securedata.New(keys, logger)
	s, err := NewService(api, logger, codec, nil, serviceConfig())
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if err := root.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	student := func(verified bool) (string, string) {
		t.Helper()
		id, token := uuid.NewString(), uuid.NewString()
		status := "unverified"
		if verified {
			status = "verified"
		}
		if err := root.Create(&entity.Account{ID: id, Nickname: "合成用户", Verification: status}).Error; err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(token))
		if err := root.Create(&entity.Session{ID: uuid.NewString(), AccountID: &id, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
		return id, token
	}
	count := func(model any) int64 {
		t.Helper()
		var n int64
		if err := root.Model(model).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	assertNoWrite := func(run func() (CreatedDraft, error), want error) {
		t.Helper()
		before := []int64{count(&entity.Wish{}), count(&entity.WishVersion{}), count(&entity.ContentRegistry{}), count(&entity.CommandReceipt{})}
		out, failure := run()
		if !errors.Is(failure, want) || !reflect.DeepEqual(out, CreatedDraft{}) {
			t.Fatalf("失败结果: %+v %v", out, failure)
		}
		after := []int64{count(&entity.Wish{}), count(&entity.WishVersion{}), count(&entity.ContentRegistry{}), count(&entity.CommandReceipt{})}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("失败留下部分写入", before, after)
		}
	}
	owner, token := student(true)
	input := draftInput()
	input.IdentityTags = &IdentityTags{Gender: pointer("female")}
	created, err := s.Create(ctx, token, "create-key", "create", input)
	if err != nil || created.State != Draft || created.Version != 1 || created.ContentVersion != 1 {
		t.Fatal(created, err)
	}
	var row entity.Wish
	if err := root.Where("id=?", created.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.OwnerID != owner || row.ExpiresAt != nil || row.PublishedAt != nil || row.DurationDays != 7 {
		t.Fatal("聚合初始化错误")
	}
	var version entity.WishVersion
	if err := root.Where("wish_id=?", created.ID).Take(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version.PublicBody != nil || string(version.IdentityTags) != "{}" || version.SchoolRelated != nil || version.SchoolDetectionState != "pending" {
		t.Fatal("未审输入成为公开投影")
	}
	var envelope securedata.Envelope
	if err := json.Unmarshal(version.BodyCipher, &envelope); err != nil {
		t.Fatal(err)
	}
	plain, err := codec.Decrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: created.ID, Version: 1}, envelope)
	if err != nil {
		t.Fatal(err)
	}
	var content Content
	if err := json.Unmarshal(plain, &content); err != nil {
		t.Fatal(err)
	}
	clear(plain)
	if content.Body != input.Body || *content.IdentityTags.Gender != "female" {
		t.Fatal("私有内容往返错误")
	}
	if _, err := codec.Decrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: uuid.NewString(), Version: 1}, envelope); err == nil {
		t.Fatal("跨对象密文替换成功")
	}
	if count(&entity.Wish{}) != 1 || count(&entity.ContentRegistry{}) != 1 || count(&entity.WorkItem{}) != 0 || count(&entity.OutboxEvent{}) != 0 {
		t.Fatal("创建不应送审或创建额外聚合")
	}
	// 相同账号/路由/键并发只产生一个结果，重放不重复调用密钥服务。
	keys.failed = true
	replayed, err := s.Create(ctx, token, "create-key", "replay", input)
	if err != nil || replayed != created {
		t.Fatal(replayed, err)
	}
	keys.failed = false
	changed := input
	changed.Body += "改变"
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "create-key", "different", changed) }, command.ErrIdempotencyConflict)
	invalid := input
	invalid.Body = ""
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "invalid", "invalid", invalid) }, command.ErrInvalid)
	_, unverified := student(false)
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, unverified, "unverified", "unverified", input) }, command.ErrForbidden)
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, "bad-token", "unknown", "unknown", input) }, command.ErrUnauthenticated)
	keys.failed = true
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "kms-fail", "kms", input) }, command.ErrUnavailable)
	keys.failed = false
	keys.panicWrap = true
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "kms-panic", "panic", input) }, command.ErrInternal)
	keys.panicWrap = false
	// 第三个写入点失败时，前两个写入及回执必须一起回滚。
	exec(`CREATE FUNCTION public.reject_synthetic_registry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic rollback'; END $$`)
	exec(`CREATE TRIGGER reject_synthetic_registry BEFORE INSERT ON public.content_registry FOR EACH ROW EXECUTE FUNCTION public.reject_synthetic_registry()`)
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "rollback", "rollback", input) }, command.ErrUnavailable)
	exec(`DROP TRIGGER reject_synthetic_registry ON public.content_registry`)
	// 延迟触发器在Resolve取得结果后、根事务提交时失败，服务不得返回成功结果或成功日志。
	exec(`CREATE CONSTRAINT TRIGGER reject_synthetic_commit AFTER INSERT ON public.content_registry DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.reject_synthetic_registry()`)
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "commit-fail", "commit-fail", input) }, command.ErrUnavailable)
	entriesBefore := logs.FilterField(zap.String("operation", "create_draft")).FilterField(zap.String("request_id", "commit-fail")).All()
	if len(entriesBefore) != 1 || entriesBefore[0].ContextMap()["result"] != "failure" {
		t.Fatal("提交失败误记成功")
	}
	exec(`DROP TRIGGER reject_synthetic_commit ON public.content_registry`)
	exec(`DROP FUNCTION public.reject_synthetic_registry()`)
	const concurrent = 6
	var wg sync.WaitGroup
	results := make(chan CreatedDraft, concurrent)
	failures := make(chan error, concurrent)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := s.Create(ctx, token, "concurrent", "concurrent", input)
			results <- out
			failures <- e
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for failure := range failures {
		if failure != nil {
			t.Fatal(failure)
		}
	}
	var first string
	for out := range results {
		if first == "" {
			first = out.ID
		}
		if out.ID != first {
			t.Fatal("并发产生不同草稿")
		}
	}
	if count(&entity.Wish{}) != 2 {
		t.Fatal("并发重复创建")
	}
	// 幂等键按当前主体隔离；回执目标失去归属时不可读，也不能借回执创建替代对象。
	otherOwner, otherToken := student(true)
	otherResult, err := s.Create(ctx, otherToken, "create-key", "other-owner", input)
	if err != nil || otherResult.ID == created.ID {
		t.Fatal(otherResult, err)
	}
	exec("UPDATE public.wishes SET owner_id=? WHERE id=?", otherOwner, created.ID)
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "create-key", "ownership", input) }, command.ErrNotFound)
	exec("UPDATE public.wishes SET owner_id=? WHERE id=?", owner, created.ID)
	// 当前资格、处罚与会话撤销优先于回执；重放不得恢复旧授权。
	exec("UPDATE public.accounts SET verification='unverified' WHERE id=?", owner)
	assertNoWrite(func() (CreatedDraft, error) {
		return s.Create(ctx, token, "create-key", "revoked-qualification", input)
	}, command.ErrForbidden)
	exec("UPDATE public.accounts SET verification='verified' WHERE id=?", owner)
	if err := root.Create(&entity.Penalty{ID: uuid.NewString(), AccountID: owner, Level: "severe", ActionKind: "ban", ViolationRef: uuid.NewString(), ReasonRef: uuid.NewString(), ContentVersion: 1, StartsAt: time.Now().Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "create-key", "penalty", input) }, command.ErrForbidden)
	exec("UPDATE public.penalties SET revoked_at=clock_timestamp() WHERE account_id=?", owner)
	// 回执解析读取当前版本，不重放历史草稿快照。
	exec("UPDATE public.wishes SET version=2 WHERE id=?", created.ID)
	current, err := s.Create(ctx, token, "create-key", "current-result", input)
	if err != nil || current.Version != 2 {
		t.Fatal(current, err)
	}
	exec("UPDATE public.sessions SET revoked_at=clock_timestamp() WHERE account_id=?", owner)
	assertNoWrite(func() (CreatedDraft, error) { return s.Create(ctx, token, "create-key", "revoked-session", input) }, command.ErrUnauthenticated)
	for _, entry := range logs.All() {
		encoded, _ := json.Marshal(entry.ContextMap())
		for _, secret := range []string{input.Body, token, "SYNTHETIC_SECRET", "synthetic-only"} {
			if strings.Contains(string(encoded), secret) || strings.Contains(entry.Message, secret) {
				t.Fatal("日志泄露敏感值")
			}
		}
	}
	entries := logs.FilterField(zap.String("operation", "create_draft")).All()
	if len(entries) == 0 {
		t.Fatal("缺少服务完成日志")
	}
}
