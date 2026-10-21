package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// wishHTTPKeys 仅用于合成夹具，不模拟真实KMS已交付或生产安全性。
type wishHTTPKeys struct{}

func (wishHTTPKeys) Wrap(_ context.Context, key []byte) (string, []byte, error) {
	return "synthetic-only", append([]byte(nil), key...), nil
}
func (wishHTTPKeys) Unwrap(_ context.Context, _ string, key []byte) ([]byte, error) {
	return append([]byte(nil), key...), nil
}

// TestPostgresWishHTTP 追溯 T014/REQ-003/004/005/018：HTTP到实际PG事务；不含前端或真实供应商联调。
func TestPostgresWishHTTP(t *testing.T) {
	dsn := os.Getenv("APP_WISH_HTTP_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PostgreSQL：python3 scripts/check-wish.py")
	}
	logger := zap.NewNop()
	open := func(role string) *gorm.DB {
		t.Helper()
		connection := dsn
		if role != "" {
			connection += " options='-c role=" + role + "'"
		}
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: connection, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if err != nil {
			t.Fatal("隔离PG连接失败")
		}
		pool, _ := db.DB()
		t.Cleanup(func() { pool.Close() })
		return db
	}
	root := open("")
	if err := migration.Run(context.Background(), root, logger, "up"); err != nil {
		t.Fatal(err)
	}
	if err := migration.ApplyPermissions(context.Background(), root, logger); err != nil {
		t.Fatal(err)
	}
	codec, _ := securedata.New(wishHTTPKeys{}, logger)
	service, err := wish.NewService(open("liyuanci_api"), logger, codec, nil, wish.ServiceConfig{
		Command: command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}, ReceiptKey: bytes.Repeat([]byte{58}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	id, token := uuid.NewString(), uuid.NewString()
	if err := root.Create(&entity.Account{ID: id, Nickname: "合成用户", Verification: "verified"}).Error; err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(token))
	if err := root.Create(&entity.Session{ID: uuid.NewString(), AccountID: &id, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	router := New(logger, func(context.Context) error { return nil })
	if err := RegisterWishCreation(router, service); err != nil {
		t.Fatal(err)
	}
	request := func(key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/wishes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	const body = `{"category":"help","body":"合成私有正文","campus":"any"}`
	first, replay := request("key", body), request("key", body)
	if first.Code != 201 || replay.Code != 201 {
		t.Fatal(first.Code, replay.Code, first.Body.String())
	}
	var a, b struct {
		Data struct {
			ID    string `json:"id"`
			State string `json:"current_state"`
		} `json:"data"`
	}
	if json.Unmarshal(first.Body.Bytes(), &a) != nil || json.Unmarshal(replay.Body.Bytes(), &b) != nil || a.Data.ID == "" || a != b || a.Data.State != "draft" {
		t.Fatal("重放结果错误")
	}
	if request("key", strings.Replace(body, "合成私有正文", "其他正文", 1)).Code != 409 {
		t.Fatal("同键异正文未冲突")
	}
	if request("bad", strings.Replace(body, "合成私有正文", strings.Repeat("心", 501), 1)).Code != 400 {
		t.Fatal("超长正文未拒绝")
	}
	var count int64
	if err := root.Model(&entity.Wish{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("请求未原子去重", count, err)
	}
	if err := root.Model(&entity.Account{}).Where("id=?", id).Update("verification", "unverified").Error; err != nil {
		t.Fatal(err)
	}
	if request("key", body).Code != 403 {
		t.Fatal("撤销资格后仍可重放")
	}
}
