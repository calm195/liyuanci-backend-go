package studentapi

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/verification"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// unavailable 只用于证明构造及未授权请求不调用供应商，不实现合成业务成功路径。
type unavailable struct{}

func (unavailable) Exchange(context.Context, string) ([]byte, error) {
	panic("供应商不应被调用")
}
func (unavailable) ResolveOpenID(context.Context, *gorm.DB, []byte) (string, error) {
	panic("身份不应被调用")
}
func (unavailable) BindStudentNumber(context.Context, *gorm.DB, string, []byte) error {
	panic("身份不应被调用")
}
func (unavailable) ReserveStudentNumber(context.Context, *gorm.DB, string, []byte) error {
	panic("身份不应被调用")
}
func (unavailable) Allow(context.Context, string) (bool, error) { panic("限流不应被调用") }
func (unavailable) PutOnce(context.Context, string, []byte) (string, error) {
	panic("对象不应被调用")
}
func (unavailable) Open(context.Context, string, string) (io.ReadCloser, error) {
	panic("对象不应被调用")
}
func (unavailable) Scan(context.Context, []byte, string) (bool, error) {
	panic("扫描不应被调用")
}
func (unavailable) Validate(context.Context, string, string) error { panic("规则不应被调用") }
func (unavailable) Wrap(context.Context, []byte) (string, []byte, error) {
	panic("密钥不应被调用")
}
func (unavailable) Unwrap(context.Context, string, []byte) ([]byte, error) {
	panic("密钥不应被调用")
}

func dependencies(t *testing.T) (Dependencies, Config) {
	t.Helper()
	pool := func() *gorm.DB {
		// 禁止自动 Ping；若构造器执行任何 SQL，本机未启动的隔离地址会使测试失败。
		db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=unused dbname=unused sslmode=disable connect_timeout=1"), &gorm.Config{DisableAutomaticPing: true, Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	logger := zap.NewNop()
	codec, err := securedata.New(unavailable{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	d := Dependencies{
		StudentDB: pool(), ReviewDB: pool(), Logger: logger,
		Ready:    func(context.Context) error { return command.ErrUnavailable },
		Exchange: unavailable{}, Identity: unavailable{}, Limiter: unavailable{}, Codec: codec,
		Objects: unavailable{}, Scanner: unavailable{}, Rules: unavailable{},
	}
	cmd := command.Config{Timeout: time.Second, LockTimeout: time.Millisecond, ReceiptTTL: time.Hour, MaxAttempts: 2}
	cfg := Config{
		Auth:         studentauth.Config{SessionTTL: time.Hour, ExchangeTTL: time.Minute, SessionKey: bytes.Repeat([]byte{1}, 32), Command: cmd},
		Card:         studentcard.Config{DecodeSlots: 1, MaxBytes: 4096, MaxPixels: 1024, ReceiptKey: bytes.Repeat([]byte{2}, 32), Command: cmd, Lease: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 2},
		Verification: verification.Config{Command: cmd, ReceiptKey: bytes.Repeat([]byte{3}, 32), MaxCardBytes: 4096},
	}
	return d, cfg
}

// TestAuthenticationRoutes 覆盖 T008/T011、REQ-002/015/018/019：完整注册、未授权及整体未就绪行为。
func TestAuthenticationRoutes(t *testing.T) {
	d, cfg := dependencies(t)
	router, err := New(d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.StudentCardAssets || cfg.Auth.VerificationRequests {
		t.Fatal("修改了调用方动作配置")
	}
	if len(router.Routes()) != 14 {
		t.Fatalf("路由不完整：%d", len(router.Routes()))
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200}, {"GET", "/readyz", 503},
		{"POST", "/api/v1/auth/wechat-sessions", 400},
		{"GET", "/api/v1/me", 401}, {"PUT", "/api/v1/me/profile", 401}, {"DELETE", "/api/v1/auth/session", 401},
		{"POST", "/api/v1/me/student-card-assets", 401}, {"GET", "/api/v1/me/student-card-assets/id", 401},
		{"POST", "/api/v1/me/verification-requests", 401}, {"GET", "/api/v1/me/verification-request", 401},
		{"GET", "/api/v1/admin/identity-reviews", 401}, {"GET", "/api/v1/admin/identity-reviews/id", 401},
		{"GET", "/api/v1/admin/identity-reviews/id/student-card", 401}, {"POST", "/api/v1/admin/identity-reviews/id/decision", 401},
		{"POST", "/api/v1/wishes", 404},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.status {
			t.Errorf("%s %s：%d", tc.method, tc.path, response.Code)
		}
	}
}

// TestRejectPartialApplication 验证配置失败不返回半套入口，拒绝共池及跨用途复用密钥。
func TestRejectPartialApplication(t *testing.T) {
	for _, name := range []string{"logger", "ready", "pool", "shared_pool", "exchange", "identity", "objects", "scanner", "rules", "codec", "auth_config", "card_config", "review_config", "shared_key", "card_budget"} {
		t.Run(name, func(t *testing.T) {
			d, cfg := dependencies(t)
			core, logs := observer.New(zap.InfoLevel)
			d.Logger = zap.New(core)
			switch name {
			case "logger":
				d.Logger = nil
			case "ready":
				d.Ready = nil
			case "pool":
				d.StudentDB = nil
			case "shared_pool":
				d.ReviewDB = d.StudentDB.Session(&gorm.Session{})
			case "exchange":
				d.Exchange = nil
			case "identity":
				d.Identity = nil
			case "objects":
				d.Objects = nil
			case "scanner":
				d.Scanner = nil
			case "rules":
				d.Rules = nil
			case "codec":
				d.Codec = nil
			case "auth_config":
				cfg.Auth.ExchangeTTL = 0
			case "card_config":
				cfg.Card.DecodeSlots = 0
			case "review_config":
				cfg.Verification.MaxCardBytes = 0
			case "shared_key":
				cfg.Card.ReceiptKey = bytes.Clone(cfg.Auth.SessionKey)
			case "card_budget":
				cfg.Verification.MaxCardBytes = cfg.Card.MaxBytes - 1
			}
			router, err := New(d, cfg)
			if err != command.ErrInvalid || router != nil {
				t.Fatal("无效依赖返回可用入口", err)
			}
			if name != "logger" && logs.FilterMessage("学生认证应用装配完成").All()[0].ContextMap()["result"] != "failure" {
				t.Fatal("缺少失败完成日志")
			}
		})
	}
}

// TestOptionalWishAssembly 追溯 T014/REQ-003/004/018：独立池/密钥、全部路由及未授权请求不调用供应商。
func TestOptionalWishAssembly(t *testing.T) {
	d, cfg := dependencies(t)
	other, _ := dependencies(t)
	d.WishDB = other.StudentDB
	cfg.Wish = &wish.ServiceConfig{Command: cfg.Auth.Command, ReceiptKey: bytes.Repeat([]byte{4}, 32)}
	router, err := New(d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Wishes {
		t.Fatal("修改调用方配置")
	}
	if len(router.Routes()) != 23 {
		t.Fatal("心愿路由不完整", len(router.Routes()))
	}
	for _, tc := range []struct{ method, path string }{{"POST", "/api/v1/wishes"}, {"PUT", "/api/v1/wishes/id/content"}, {"POST", "/api/v1/wishes/id/submit-review"}, {"POST", "/api/v1/wishes/id/publish"}, {"POST", "/api/v1/wishes/id/withdraw"}, {"DELETE", "/api/v1/wishes/id/draft"}, {"GET", "/api/v1/me/wishes"}} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != 401 {
			t.Fatal(tc, response.Code)
		}
	}
	d.WishDB = d.StudentDB
	if router, err := New(d, cfg); err == nil || router != nil {
		t.Fatal("允许复用身份维护池")
	}
	d.WishDB = other.StudentDB
	cfg.Wish.ReceiptKey = cfg.Auth.SessionKey
	if router, err := New(d, cfg); err == nil || router != nil {
		t.Fatal("允许复用用途密钥")
	}
}
