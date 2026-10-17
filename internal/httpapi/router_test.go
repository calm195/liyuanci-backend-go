package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestFoundationBoundary 验证 T004/REQ-018、020：无认证演示入口关闭，失败/恢复不泄漏且保留完成日志。
func TestFoundationBoundary(t *testing.T) {
	for _, tc := range []struct {
		path             string
		fail, panicReady bool
		status           int
	}{
		{path: "/healthz", status: 200}, {path: "/readyz", status: 200},
		{path: "/readyz", fail: true, status: 503}, {path: "/readyz", panicReady: true, status: 500},
		{path: "/api/v1/users", status: 404}, {path: "/api/v1/me", status: 404},
	} {
		t.Run(tc.path, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			router := New(zap.New(core), func(context.Context) error {
				if tc.panicReady {
					panic("secret body")
				}
				if tc.fail {
					return errors.New("secret DSN")
				}
				return nil
			})
			response := httptest.NewRecorder()
			req := httptest.NewRequest("GET", tc.path, nil)
			req.Header.Set("Authorization", "Bearer secret-token")
			router.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			entries := logs.FilterMessage("http request completed").All()
			if len(entries) != 1 || entries[0].ContextMap()["status"] != int64(tc.status) || entries[0].ContextMap()["request_id"] != response.Header().Get("X-Request-ID") {
				t.Fatal("missing completion correlation")
			}
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatal("response leaked")
			}
			for _, entry := range logs.All() {
				for _, f := range entry.Context {
					if strings.Contains(f.String, "secret") {
						t.Fatal("log leaked")
					}
				}
			}
		})
	}
}
