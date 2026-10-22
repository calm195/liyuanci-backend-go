package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// TestApplicationProtocolValidation 验证T019所有写请求严格字段、必填版本及查询边界，非法输入不进入服务。
func TestApplicationProtocolValidation(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/wishes/id/applications", `{"message":"x"}`},
		{"POST", "/api/v1/wishes/id/applications", `{"message":"x","wish_content_version":0}`},
		{"POST", "/api/v1/wishes/id/applications", `{"message":"x","wish_content_version":1,"author":"attacker"}`},
		{"POST", "/api/v1/wishes/id/applications", `{"message":"x","message":"y","wish_content_version":1}`},
		{"POST", "/api/v1/wishes/id/applications", `{"message":null,"wish_content_version":1}`},
		{"POST", "/api/v1/applications/id/withdraw", `{"Expected_version":1}`},
		{"POST", "/api/v1/applications/id/withdraw", `{"expected_version":1.5}`},
		{"POST", "/api/v1/applications/id/accept", `{"expected_version":1}`},
		{"POST", "/api/v1/applications/id/accept", `{"expected_version":1,"wish_expected_version":0}`},
		{"POST", "/api/v1/wishes/id/rollback", `{"expected_version":1,"target":"matching"}`},
		{"POST", "/api/v1/wishes/id/rollback", `{"expected_version":1,"target":"fulfilled","refresh_expiry":false}`},
		{"GET", "/api/v1/me/applications?limit=0", ""},
		{"GET", "/api/v1/me/applications?limit=1&limit=2", ""},
		{"GET", "/api/v1/wishes/id/applications?status=pending_scan", ""},
		{"GET", "/api/v1/me/applications?cursor=", ""},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			r := New(zap.New(core), nil)
			if e := RegisterApplications(r, &wish.Service{}); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set("Idempotency-Key", "synthetic")
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			if out.Code != 400 || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(out.Code, out.Body.String())
			}
			if logs.FilterField(zap.String("event", "http_request_completed")).Len() != 1 {
				t.Fatal("拒绝路径缺请求完成日志")
			}
		})
	}
	if RegisterApplications(nil, &wish.Service{}) != command.ErrInvalid {
		t.Fatal("接受无效装配")
	}
}

// TestApplicationBusinessErrors 验证独立409错误码，不用通用状态冲突代替额度/待审/去重。
func TestApplicationBusinessErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{command.ErrActiveWishLimit, "ACTIVE_WISH_LIMIT"}, {command.ErrDuplicateApplication, "DUPLICATE_APPLICATION"}, {command.ErrBlockedInteraction, "BLOCKED_INTERACTION"}, {command.ErrReviewPending, "REVIEW_PENDING"}} {
		t.Run(tc.code, func(t *testing.T) {
			r := New(zap.NewNop(), nil)
			r.GET("/failure", func(c *gin.Context) { adminError(c, tc.err) })
			out := httptest.NewRecorder()
			r.ServeHTTP(out, httptest.NewRequest("GET", "/failure", nil))
			if out.Code != 409 || !strings.Contains(out.Body.String(), tc.code) {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
}
