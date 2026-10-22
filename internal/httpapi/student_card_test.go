package httpapi

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
)

type cardsFixture struct {
	failure error
	called  bool
}

func (f *cardsFixture) Upload(context.Context, string, string, string, string, io.Reader) (studentcard.Asset, error) {
	f.called = true
	return studentcard.Asset{}, f.failure
}
func (f *cardsFixture) Get(context.Context, string, string, string) (studentcard.Asset, error) {
	f.called = true
	return studentcard.Asset{}, f.failure
}

// T011/REQ-002、018：鉴权/幂等键/媒体边界与状态错误映射；有效multipart由真实服务测试验证。
func TestStudentCardProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, token, key, contentType string
		failure                                     error
		status                                      int
		called                                      bool
	}{
		{name: "no_token", method: "POST", path: "", status: 401},
		{name: "no_key", method: "POST", path: "", token: "token", status: 400},
		{name: "not_multipart", method: "POST", path: "", token: "token", key: "key", contentType: "application/json", status: 400},
		{name: "status", method: "GET", path: "/00000000-0000-4000-8000-000000000001", token: "token", status: 200, called: true},
		{name: "not_found", method: "GET", path: "/00000000-0000-4000-8000-000000000001", token: "token", status: 404, called: true, failure: command.ErrNotFound},
		{name: "unauthenticated", method: "GET", path: "/00000000-0000-4000-8000-000000000001", token: "token", status: 401, called: true, failure: command.ErrUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := New(zap.NewNop(), func(context.Context) error { return nil })
			fixture := &cardsFixture{failure: tc.failure}
			if e := RegisterStudentCards(router, fixture); e != nil {
				t.Fatal(e)
			}
			request := httptest.NewRequest(tc.method, "/api/v1/me/student-card-assets"+tc.path, strings.NewReader("{}"))
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				request.Header.Set("Idempotency-Key", tc.key)
			}
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request)
			if rec.Code != tc.status || fixture.called != tc.called {
				t.Fatal(rec.Code, fixture.called)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("缺no-store")
			}
		})
	}
	if RegisterStudentCards(nil, &cardsFixture{}) == nil {
		t.Fatal("nil装配未拒绝")
	}
}
