package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
)

type restoreFixture struct {
	adminFixture
	token string
}

func (f *restoreFixture) Restore(_ context.Context, token, _ string) (adminauth.Session, error) {
	f.called = true
	f.token = token
	return adminauth.Session{Token: token}, f.failure
}

// TestAdminSessionRestore 验证恢复必须携带同源自定义头，过期会话清除Cookie。
func TestAdminSessionRestore(t *testing.T) {
	for _, tc := range []struct {
		name, origin, site, header, cookie string
		failure                            error
		want                               int
		called                             bool
	}{
		{name: "正常恢复", header: "1", cookie: "token", want: 200, called: true},
		{name: "缺少自定义头", cookie: "token", want: 403},
		{name: "协议不同", origin: "http://admin.test", header: "1", cookie: "token", want: 403},
		{name: "依赖故障保留会话", header: "1", cookie: "token", failure: adminauth.ErrUnavailable, want: 503, called: true},
		{name: "跨源", origin: "https://evil.test", header: "1", cookie: "token", want: 403},
		{name: "同站不同源", site: "same-site", header: "1", cookie: "token", want: 403},
		{name: "缺少Cookie", header: "1", want: 401},
		{name: "已撤销", header: "1", cookie: "token", failure: adminauth.ErrUnauthenticated, want: 401, called: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &restoreFixture{adminFixture: adminFixture{failure: tc.failure}}
			r := New(zap.NewNop(), func(context.Context) error { return nil })
			if err := RegisterAdmin(r, f); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "https://admin.test/api/v1/admin/session", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Sec-Fetch-Site", tc.site)
			req.Header.Set("X-Session-Restore", tc.header)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want || f.called != tc.called {
				t.Fatalf("恢复状态错误：%d %v", w.Code, f.called)
			}
			if tc.failure == adminauth.ErrUnavailable && w.Header().Get("Set-Cookie") != "" {
				t.Fatal("依赖故障不能清除Cookie")
			}
			if tc.failure == adminauth.ErrUnauthenticated && !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
				t.Fatal("未清除失效Cookie")
			}
		})
	}
}

// TestAdminCookieFlags 确认正式站点使用安全Cookie，本地开发可通过HTTP恢复。
func TestAdminCookieFlags(t *testing.T) {
	for _, host := range []string{"https://admin.test", "http://localhost:5173"} {
		r := New(zap.NewNop(), func(context.Context) error { return nil })
		if err := RegisterAdmin(r, &adminFixture{}); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", host+"/api/v1/admin/sessions", strings.NewReader(`{"login_name":"admin","password":"synthetic"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/api/v1/admin" {
			t.Fatal("Cookie属性错误")
		}
		if cookies[0].Secure != strings.HasPrefix(host, "https:") {
			t.Fatal("安全Cookie标记错误")
		}
	}
}
