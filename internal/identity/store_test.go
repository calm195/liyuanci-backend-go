package identity

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// 合成包裹密钥与检索密钥彼此独立，仅用于 T005/REQ-002、018 的协议测试。
type syntheticKeys struct {
	unavailable bool
	wrapRef     string
}

func (k *syntheticKeys) Wrap(_ context.Context, data []byte) (string, []byte, error) {
	if k.unavailable {
		return "", nil, errors.New("SYNTHETIC_PROVIDER_SECRET")
	}
	ref := k.wrapRef
	if ref == "" {
		ref = "synthetic-wrap"
	}
	material := byte(91)
	if ref == "synthetic-wrap-new" {
		material = 92
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{material}, 32))
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	return ref, append(nonce, gcm.Seal(nil, nonce, data, nil)...), nil
}
func (k *syntheticKeys) Unwrap(_ context.Context, ref string, wrapped []byte) ([]byte, error) {
	if k.unavailable || (ref != "synthetic-wrap" && ref != "synthetic-wrap-new") || len(wrapped) < 28 {
		return nil, errors.New("SYNTHETIC_PROVIDER_SECRET")
	}
	material := byte(91)
	if ref == "synthetic-wrap-new" {
		material = 92
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{material}, 32))
	gcm, _ := cipher.NewGCM(block)
	return gcm.Open(nil, wrapped[:12], wrapped[12:], nil)
}
func (k *syntheticKeys) Digest(_ context.Context, ref, purpose string, value []byte) ([]byte, error) {
	if k.unavailable {
		return nil, errors.New("SYNTHETIC_PROVIDER_SECRET")
	}
	var key []byte
	switch ref {
	case "synthetic-old":
		key = bytes.Repeat([]byte{11}, 32)
	case "synthetic-new":
		key = bytes.Repeat([]byte{22}, 32)
	case "synthetic-third":
		key = bytes.Repeat([]byte{33}, 32)
	default:
		return nil, errors.New("SYNTHETIC_PROVIDER_SECRET")
	}
	return securedata.LookupDigest(key, purpose, string(value))
}

func TestMissingDependencies(t *testing.T) {
	if _, err := New(nil, nil, nil, zap.NewNop()); err == nil {
		t.Fatal("依赖缺失应失败")
	}
}

func TestPostgresIdentity(t *testing.T) {
	// T005/REQ-002、018、020：专用 Docker 数据库中验证真实事务和唯一约束，无真实个人资料。
	dsn := os.Getenv("APP_MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式专用 PostgreSQL；运行 python3 scripts/check-migrations.py")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal("测试数据库连接失败")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	core, observed := observer.New(zap.InfoLevel)
	log := zap.New(core)
	ctx := context.Background()
	if err = migration.Run(ctx, db, log, "up"); err != nil {
		t.Fatal(err)
	}
	keys := &syntheticKeys{}
	codec, err := securedata.New(keys, log)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(db, codec, keys, log)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 10)
	for i := range ids {
		ids[i] = fmt.Sprintf("10000000-0000-0000-0000-%012d", i+1)
		if err = db.Exec("INSERT INTO public.accounts(id,nickname) VALUES (?,?)", ids[i], "合成账号").Error; err != nil {
			t.Fatal(err)
		}
	}
	value := []byte("SYNTHETIC_STUDENT_NUMBER")
	if !errors.Is(store.Bind(ctx, ids[0], "student_number", value), ErrUnavailable) {
		t.Fatal("未配置检索代次必须失败")
	}
	for _, purpose := range []string{"student_number", "openid"} {
		if err = store.Configure(ctx, purpose, "synthetic-old"); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Configure(ctx, "student_number", "synthetic-new"); !errors.Is(err, ErrConflict) {
		t.Fatal("禁止直接替换检索配置")
	}
	type outcome struct {
		id  string
		err error
	}
	results := make(chan outcome, 8)
	var wg sync.WaitGroup
	for _, id := range ids[:8] {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			results <- outcome{id: id, err: store.Bind(ctx, id, "student_number", value)}
		}(id)
	}
	wg.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			if winner != "" {
				t.Fatal("同学号不能有两个占用者")
			}
			winner = result.id
		} else if !errors.Is(result.err, ErrConflict) {
			t.Fatal(result.err)
		}
	}
	if winner == "" {
		t.Fatal("应有且仅有一个绑定成功")
	}
	if err = store.Bind(ctx, winner, "student_number", value); err != nil {
		t.Fatal("同值重放应成功", err)
	}
	if !errors.Is(store.Bind(ctx, winner, "student_number", []byte("CHANGED_VALUE")), ErrConflict) {
		t.Fatal("不能静默改号")
	}
	var payload []struct{ Envelope []byte }
	if err = db.Raw("SELECT envelope FROM public.identity_secrets WHERE account_id = ? AND purpose = 'student_number'", winner).Scan(&payload).Error; err != nil || len(payload) != 1 {
		t.Fatal("缺少身份密文")
	}
	if bytes.Contains(payload[0].Envelope, value) {
		t.Fatal("身份值不得明文落库")
	}
	var envelope securedata.Envelope
	if json.Unmarshal(payload[0].Envelope, &envelope) != nil {
		t.Fatal("密文格式错误")
	}
	plain, err := codec.Decrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: winner, Version: 1}, envelope)
	if err != nil || !bytes.Equal(plain, value) {
		t.Fatal("加密落库不能还原", err)
	}
	clear(plain)
	if err = store.BeginRotation(ctx, "student_number", "synthetic-new"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(store.BeginRotation(ctx, "student_number", "synthetic-third"), ErrConflict) {
		t.Fatal("禁止第三代重叠")
	}
	if !errors.Is(store.FinishRotation(ctx, "student_number"), ErrConflict) {
		t.Fatal("未回填禁止移除旧代")
	}
	if !errors.Is(store.Bind(ctx, ids[8], "student_number", value), ErrConflict) {
		t.Fatal("轮换期间旧代占用仍须唯一")
	}
	if err = store.Bind(ctx, ids[8], "student_number", []byte("SYNTHETIC_SECOND_NUMBER")); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = db.Table("public.identity_lookup_claims").Where("account_id = ? AND purpose = ?", ids[8], "student_number").Count(&count).Error; err != nil || count != 2 {
		t.Fatal("轮换期间新绑定必须双写")
	}
	// 失钥批次必须回滚；后续恢复可继续，不清除旧代占用。
	keys.unavailable = true
	if n, e := store.Backfill(ctx, "student_number", 1); e == nil || n != 0 {
		t.Fatal("失钥不能回填")
	}
	if e := store.Bind(ctx, ids[9], "student_number", []byte("SYNTHETIC_THIRD_NUMBER")); e == nil {
		t.Fatal("失钥不能写入")
	}
	keys.unavailable = false
	if err = db.Exec("UPDATE public.identity_secrets SET envelope = '{}'::jsonb WHERE account_id = ? AND purpose = ?", winner, "student_number").Error; err != nil {
		t.Fatal(err)
	}
	if n, e := store.Backfill(ctx, "student_number", 1); e == nil || n != 0 {
		t.Fatal("损坏密文不能回填")
	}
	if err = db.Exec("UPDATE public.identity_secrets SET envelope = ?::jsonb WHERE account_id = ? AND purpose = ?", string(payload[0].Envelope), winner, "student_number").Error; err != nil {
		t.Fatal(err)
	}
	if n, e := store.Backfill(ctx, "student_number", 1); e != nil || n != 1 {
		t.Fatal("恢复后应回填一条", n, e)
	}
	if n, e := store.Backfill(ctx, "student_number", 1); e != nil || n != 0 {
		t.Fatal("回填应幂等", n, e)
	}
	if err = store.FinishRotation(ctx, "student_number"); err != nil {
		t.Fatal(err)
	}
	if err = db.Table("public.identity_lookup_claims").Where("purpose = ? AND key_ref = ?", "student_number", "synthetic-old").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("完成轮换后应清除旧代")
	}
	if !errors.Is(store.Bind(ctx, ids[9], "student_number", value), ErrConflict) {
		t.Fatal("新代继续唯一")
	}
	if err = store.Bind(ctx, ids[9], "openid", value); err != nil {
		t.Fatal("不同用途独立", err)
	}
	if err = db.Exec("UPDATE public.accounts SET lifecycle = 'deleting' WHERE id = ?", ids[9]).Error; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(store.Bind(ctx, ids[9], "student_number", []byte("SYNTHETIC_THIRD_NUMBER")), ErrUnavailable) {
		t.Fatal("注销中禁止绑定")
	}
	// 这里只模拟普通账号清理完成后的物理删除，不冒称完整注销协议已实现。
	if err = db.Exec("DELETE FROM public.accounts WHERE id = ?", winner).Error; err != nil {
		t.Fatal(err)
	}
	// 用从未绑定的正常账号验证物理删除后释放普通身份占用。
	if err = db.Exec("UPDATE public.accounts SET lifecycle = 'active' WHERE id = ?", ids[9]).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.Bind(ctx, ids[9], "student_number", value); err != nil {
		t.Fatal("删除后应释放唯一占用", err)
	}
	// T005/REQ-018：包裹密钥重加密与检索密钥轮换独立，失败批次不改变旧密文。
	keys.unavailable = true
	if n, e := store.Reencrypt(ctx, "student_number", "synthetic-wrap-new", 1); e == nil || n != 0 {
		t.Fatal("失钥不能重加密")
	}
	keys.unavailable = false
	if n, e := store.Reencrypt(ctx, "student_number", "synthetic-wrap-new", 1); e == nil || n != 0 {
		t.Fatal("实际包裹引用不符必须回滚")
	}
	keys.wrapRef = "synthetic-wrap-new"
	for _, expected := range []int{1, 1, 0} {
		if n, e := store.Reencrypt(ctx, "student_number", "synthetic-wrap-new", 1); e != nil || n != expected {
			t.Fatal("重加密批次结果错误", n, e)
		}
	}
	var reencrypted []struct{ Envelope []byte }
	if err = db.Raw("SELECT envelope FROM public.identity_secrets WHERE account_id = ? AND purpose = ?", ids[9], "student_number").Scan(&reencrypted).Error; err != nil || len(reencrypted) != 1 {
		t.Fatal("缺少重加密结果")
	}
	if json.Unmarshal(reencrypted[0].Envelope, &envelope) != nil || envelope.KeyRef != "synthetic-wrap-new" {
		t.Fatal("新包裹引用未保存")
	}
	plain, err = codec.Decrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: ids[9], Version: 1}, envelope)
	if err != nil || !bytes.Equal(plain, value) {
		t.Fatal("重加密不应改变身份值", err)
	}
	clear(plain)
	if err = migration.ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal(err)
	}
	if err = migration.ApplyPermissions(ctx, db, log); err != nil {
		t.Fatal("权限初始化应可重放", err)
	}
	for _, check := range []struct {
		role, table, privilege string
		allowed                bool
	}{
		{"liyuanci_api", "public.accounts", "UPDATE", true},
		{"liyuanci_api", "public.identity_secrets", "INSERT", true},
		{"liyuanci_api", "public.identity_lookup_keys", "UPDATE", false},
		{"liyuanci_api", "public.schema_migrations", "SELECT", false},
		{"liyuanci_worker", "public.identity_secrets", "SELECT", false},
		{"liyuanci_worker", "public.accounts", "UPDATE", false},
		{"liyuanci_identity_maintenance", "public.identity_lookup_keys", "UPDATE", true},
	} {
		var allowed bool
		if err = db.Raw("SELECT has_table_privilege(?, ?, ?)", check.role, check.table, check.privilege).Scan(&allowed).Error; err != nil || allowed != check.allowed {
			t.Fatalf("权限矩阵不符：%s %s %s", check.role, check.table, check.privilege)
		}
	}
	// 使用真实 SET ROLE 执行，不仅检查授权目录；API 可绑定但不得切换检索代次。
	if err = db.Connection(func(conn *gorm.DB) error {
		if e := conn.Exec("SET ROLE liyuanci_identity_maintenance").Error; e != nil {
			return e
		}
		defer func() { _, _ = conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE") }()
		maintenance, e := New(conn, codec, keys, log)
		if e != nil {
			return e
		}
		if n, e := maintenance.Reencrypt(ctx, "openid", "synthetic-wrap-new", 1); e != nil || n != 1 {
			return errors.New("维护角色重加密失败")
		}
		if e = maintenance.BeginRotation(ctx, "openid", "synthetic-new"); e != nil {
			return e
		}
		if n, e := maintenance.Backfill(ctx, "openid", 1); e != nil || n != 1 {
			return errors.New("维护角色回填失败")
		}
		return maintenance.FinishRotation(ctx, "openid")
	}); err != nil {
		t.Fatal(err)
	}
	apiID := "20000000-0000-0000-0000-000000000001"
	if err = db.Exec("INSERT INTO public.accounts(id,nickname) VALUES (?,?)", apiID, "合成权限账号").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Connection(func(conn *gorm.DB) error {
		if e := conn.Exec("SET ROLE liyuanci_api").Error; e != nil {
			return e
		}
		defer func() { _, _ = conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE") }()
		apiStore, e := New(conn, codec, keys, log)
		if e != nil {
			return e
		}
		if e = apiStore.Bind(ctx, apiID, "student_number", []byte("SYNTHETIC_API_VALUE")); e != nil {
			return fmt.Errorf("API授权绑定失败：%w", e)
		}
		if e = apiStore.BeginRotation(ctx, "student_number", "synthetic-third"); e == nil {
			return errors.New("API不应有轮换配置写权限")
		}
		if e = conn.Exec("SELECT * FROM public.schema_migrations").Error; e == nil {
			return errors.New("API不应读取迁移账本")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Connection(func(conn *gorm.DB) error {
		if e := conn.Exec("SET ROLE liyuanci_worker").Error; e != nil {
			return e
		}
		defer func() { _, _ = conn.Statement.ConnPool.ExecContext(context.Background(), "RESET ROLE") }()
		if e := conn.Exec("SELECT * FROM public.identity_secrets").Error; e == nil {
			return errors.New("worker不应读取身份密文")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if observed.FilterField(zap.String("result", "failure")).Len() == 0 {
		t.Fatal("缺少失败完成日志")
	}
	// T005/REQ-018、020：实体 CRUD 与 JSONB 回写完成后仍由事务边界输出成功及耗时。
	for _, operation := range []string{"configure", "bind", "rotation_begin", "rotation_backfill", "rotation_finish", "reencrypt"} {
		entries := observed.FilterMessage("身份存储操作完成").FilterField(zap.String("operation", operation)).FilterField(zap.String("result", "success")).All()
		if len(entries) == 0 || entries[0].ContextMap()["duration"] == nil {
			t.Fatalf("缺少身份操作成功完成日志：%s", operation)
		}
	}
	for _, entry := range observed.All() {
		for _, field := range entry.ContextMap() {
			if text, ok := field.(string); ok && (strings.Contains(text, "SYNTHETIC") || strings.Contains(text, winner)) {
				t.Fatal("日志泄漏身份或供应商错误")
			}
		}
	}
}
