package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/command"
)

type adminFixture struct {
	failure    error
	called     bool
	panicLogin bool
}

func (f *adminFixture) ListAccounts(context.Context, string, string, string, int) (adminauth.AccountPage, error) {
	f.called = true
	return adminauth.AccountPage{Items: []adminauth.Account{}}, f.failure
}
func (f *adminFixture) ChangeRoles(context.Context, string, string, string, string, adminauth.RolesInput) (adminauth.Account, error) {
	f.called = true
	return adminauth.Account{}, f.failure
}

func (f *adminFixture) Create(context.Context, string, string, string, adminauth.CreateInput) (adminauth.Account, error) {
	f.called = true
	return adminauth.Account{}, f.failure
}
func (f *adminFixture) Login(context.Context, string, string, string, string) (adminauth.Session, error) {
	f.called = true
	if f.panicLogin {
		panic("SYNTHETIC_PASSWORD")
	}
	return adminauth.Session{}, f.failure
}
func (f *adminFixture) ChangePassword(context.Context, string, string, string, adminauth.PasswordInput) error {
	f.called = true
	return f.failure
}
func (f *adminFixture) Logout(context.Context, string, string, string) error {
	f.called = true
	return f.failure
}

// T008/REQ-018、019：HTTP失败与panic完成日志、严格字段、命名空间凭据和稳定错误映射。
func TestAdminProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, token, key string
		failure                              error
		want                                 int
		called, panicLogin                   bool
	}{
		{name: "list", method: "GET", path: "/accounts", token: "token", want: 200, called: true},
		{name: "list_duplicate", method: "GET", path: "/accounts?limit=1&limit=2", token: "token", want: 400},
		{name: "list_unknown", method: "GET", path: "/accounts?filter=x", token: "token", want: 400},
		{name: "list_limit", method: "GET", path: "/accounts?limit=51", token: "token", want: 400},
		{name: "roles", method: "PUT", path: "/accounts/00000000-0000-4000-8000-000000000001/roles", body: `{"roles":[],"reason":"合成","expected_version":1}`, token: "token", key: "key", want: 200, called: true},
		{name: "roles_version", method: "PUT", path: "/accounts/00000000-0000-4000-8000-000000000001/roles", body: `{"roles":[],"reason":"合成","expected_version":1}`, token: "token", key: "key", want: 409, called: true, failure: command.ErrVersionConflict},
		{name: "roles_missing", method: "PUT", path: "/accounts/00000000-0000-4000-8000-000000000001/roles", body: `{"reason":"合成","expected_version":1}`, token: "token", key: "key", want: 400},
		{name: "login", method: "POST", path: "/sessions", body: `{"login_name":"synthetic","password":"SYNTHETIC_PASSWORD"}`, want: 200, called: true},
		{name: "unknown_field", method: "POST", path: "/sessions", body: `{"login_name":"synthetic","password":"x","roles":["super_admin"]}`, want: 400},
		{name: "duplicate_field", method: "POST", path: "/sessions", body: `{"login_name":"synthetic","password":"a","password":"b"}`, want: 400},
		{name: "trailing", method: "POST", path: "/sessions", body: `{} {}`, want: 400},
		{name: "array", method: "POST", path: "/sessions", body: `[]`, want: 400},
		{name: "null", method: "POST", path: "/sessions", body: `{"login_name":null}`, want: 400},
		{name: "missing_password", method: "POST", path: "/sessions", body: `{"login_name":"synthetic"}`, want: 400},
		{name: "no_bearer", method: "POST", path: "/accounts", body: `{}`, key: "key", want: 401},
		{name: "no_key", method: "POST", path: "/accounts", body: `{}`, token: "token", want: 400},
		{name: "no_roles", method: "POST", path: "/accounts", body: `{"login_name":"x","reason":"合成"}`, token: "token", key: "key", want: 400},
		{name: "create", method: "POST", path: "/accounts", body: `{"login_name":"x","roles":[],"reason":"合成"}`, token: "token", key: "key", want: 201, called: true},
		{name: "password", method: "POST", path: "/password", body: `{"current_password":"a","new_password":"b"}`, token: "token", key: "key", want: 204, called: true},
		{name: "password_unknown", method: "POST", path: "/password", body: `{"subject_id":"x"}`, token: "token", key: "key", want: 400},
		{name: "logout", method: "DELETE", path: "/session", token: "token", key: "key", want: 204, called: true},
		{name: "panic", method: "POST", path: "/sessions", body: `{"login_name":"synthetic","password":"SYNTHETIC_PASSWORD"}`, want: 500, called: true, panicLogin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			router := New(zap.New(core), func(context.Context) error { return nil })
			fake := &adminFixture{failure: tc.failure, panicLogin: tc.panicLogin}
			if e := RegisterAdmin(router, fake); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest(tc.method, "/api/v1/admin"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want || fake.called != tc.called {
				t.Fatal("协议结果不符", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "SYNTHETIC_PASSWORD") {
				t.Fatal("敏感响应边界错误")
			}
			if tc.want == 204 && rec.Body.Len() != 0 {
				t.Fatal("204必须空体")
			}
			if logs.FilterMessage("HTTP请求处理完成").Len() != 1 {
				t.Fatal("失败或恢复路径缺完成日志")
			}
		})
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{adminauth.ErrUnauthenticated, 401, "UNAUTHENTICATED"}, {adminauth.ErrForbidden, 403, "FORBIDDEN"}, {adminauth.ErrConflict, 409, "STATE_CONFLICT"},
		{command.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"}, {adminauth.ErrRateLimited, 429, "RATE_LIMITED"}, {command.ErrNotFound, 404, "NOT_FOUND"},
		{command.ErrInternal, 500, "INTERNAL_ERROR"}, {errors.New("SYNTHETIC_PASSWORD"), 503, "DEPENDENCY_UNAVAILABLE"},
	} {
		router := New(zap.NewNop(), func(context.Context) error { return nil })
		_ = RegisterAdmin(router, &adminFixture{failure: tc.err})
		req := httptest.NewRequest("POST", "/api/v1/admin/sessions", strings.NewReader(`{"login_name":"x","password":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) || strings.Contains(rec.Body.String(), "SYNTHETIC_PASSWORD") {
			t.Fatal("错误映射或脱敏失败", rec.Code)
		}
	}
	if RegisterAdmin(nil, &adminFixture{}) == nil {
		t.Fatal("nil路由必须拒绝")
	}
}
