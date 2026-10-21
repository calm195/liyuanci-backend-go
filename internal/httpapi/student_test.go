package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
)

type studentFixture struct {
	failure    error
	called     bool
	panicLogin bool
}

func (f *studentFixture) Login(context.Context, string, string, string, string) (studentauth.Session, error) {
	f.called = true
	if f.panicLogin {
		panic("SYNTHETIC_CODE")
	}
	return studentauth.Session{}, f.failure
}
func (f *studentFixture) Current(context.Context, string, string) (studentauth.Me, error) {
	f.called = true
	return studentauth.Me{}, f.failure
}
func (f *studentFixture) UpdateProfile(context.Context, string, string, string, studentauth.ProfileInput) (studentauth.Me, error) {
	f.called = true
	return studentauth.Me{}, f.failure
}
func (f *studentFixture) Logout(context.Context, string, string, string) error {
	f.called = true
	return f.failure
}

// T008/REQ-002、014、018：微信code字段白名单、本人资料和退出的HTTP协议，不替代真实微信联调。
func TestStudentProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, token, key string
		failure                              error
		status                               int
		called, panicLogin                   bool
	}{
		{name: "login", method: "POST", path: "/auth/wechat-sessions", body: `{"code":"x","client_request_id":"r"}`, status: 200, called: true},
		{name: "openid_forbidden", method: "POST", path: "/auth/wechat-sessions", body: `{"code":"x","client_request_id":"r","openid":"y"}`, status: 400},
		{name: "missing_code", method: "POST", path: "/auth/wechat-sessions", body: `{"client_request_id":"r"}`, status: 400},
		{name: "current", method: "GET", path: "/me", token: "token", status: 200, called: true},
		{name: "missing_token", method: "GET", path: "/me", status: 401},
		{name: "profile", method: "PUT", path: "/me/profile", token: "token", key: "key", body: `{"nickname":"合成","hide_identity_tags":false,"expected_version":1}`, status: 200, called: true},
		{name: "profile_null", method: "PUT", path: "/me/profile", token: "token", key: "key", body: `{"nickname":"合成","hide_identity_tags":null,"expected_version":1}`, status: 400},
		{name: "profile_missing", method: "PUT", path: "/me/profile", token: "token", key: "key", body: `{"nickname":"合成","expected_version":1}`, status: 400},
		{name: "profile_key", method: "PUT", path: "/me/profile", token: "token", body: `{}`, status: 400},
		{name: "logout", method: "DELETE", path: "/auth/session", token: "token", key: "key", status: 204, called: true},
		{name: "logout_forbidden", method: "DELETE", path: "/auth/session", token: "token", key: "key", status: 401, called: true, failure: command.ErrUnauthenticated},
		{name: "rate_limit", method: "POST", path: "/auth/wechat-sessions", body: `{"code":"x","client_request_id":"r"}`, status: 429, called: true, failure: studentauth.ErrRateLimited},
		{name: "panic", method: "POST", path: "/auth/wechat-sessions", body: `{"code":"x","client_request_id":"r"}`, status: 500, called: true, panicLogin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(zap.NewNop(), func(context.Context) error { return nil })
			f := &studentFixture{failure: tc.failure, panicLogin: tc.panicLogin}
			if e := RegisterStudent(r, f); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.status || f.called != tc.called {
				t.Fatal(rec.Code, f.called)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("缺敏感缓存防护")
			}
			if strings.Contains(rec.Body.String(), "SYNTHETIC_CODE") {
				t.Fatal("panic泄漏")
			}
		})
	}
	if RegisterStudent(nil, &studentFixture{}) == nil {
		t.Fatal("nil路由未拒绝")
	}
}
