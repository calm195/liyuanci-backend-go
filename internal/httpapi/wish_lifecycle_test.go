package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// TestWishLifecycleValidation 追溯 T014/REQ-003/004/018：无效协议必须在访问数据库/供应商之前拒绝。
func TestWishLifecycleValidation(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", "/api/v1/wishes/id/content", `{"category":"help","body":"x","campus":"any","expected_version":1}`, 400},
		{"PUT", "/api/v1/wishes/id/content", `{"category":"help","body":"x","campus":"any","expected_version":1,"refresh_expiry":null}`, 400},
		{"PUT", "/api/v1/wishes/id/content", `{"category":"help","body":"x","campus":"any","expected_version":1,"refresh_expiry":true,"identity_tags":{"gender":null}}`, 400},
		{"POST", "/api/v1/wishes/id/submit-review", `{"expected_version":1}`, 400},
		{"POST", "/api/v1/wishes/id/submit-review", `{"expected_version":1,"content_version":0}`, 400},
		{"POST", "/api/v1/wishes/id/submit-review", `{"expected_version":1,"content_version":2,"content_version":3}`, 400},
		{"POST", "/api/v1/wishes/id/publish", `{"expected_version":null}`, 400},
		{"POST", "/api/v1/wishes/id/withdraw", `{"expected_version":1.5}`, 400},
		{"DELETE", "/api/v1/wishes/id/draft", `{"expected_version":1,"state":"published"}`, 400},
		{"GET", "/api/v1/wishes?limit=0", "", 400},
		{"GET", "/api/v1/wishes?limit=101", "", 400},
		{"GET", "/api/v1/wishes?limit=1&limit=2", "", 400},
		{"GET", "/api/v1/wishes?state=draft", "", 400},
		{"GET", "/api/v1/me/wishes?category=help", "", 400},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			r := New(zap.New(core), func(context.Context) error { return nil })
			// 零值服务不能执行任何I/O；若解析错误地放行，恢复路径会返回500而不是期望的400。
			if err := RegisterWishes(r, &wish.Service{}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set("Idempotency-Key", "synthetic")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.status || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(rec.Code, rec.Body.String())
			}
			if len(logs.FilterField(zap.String("event", "http_request_completed")).All()) != 1 {
				t.Fatal("拒绝路径缺完成日志")
			}
		})
	}
	if RegisterWishes(nil, &wish.Service{}) == nil {
		t.Fatal("接受无效装配")
	}
}
