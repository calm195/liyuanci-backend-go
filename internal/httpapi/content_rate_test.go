package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type testContentCounter func(context.Context, string) (bool, error)

func (f testContentCounter) Allow(ctx context.Context, key string) (bool, error) {
	return f(ctx, key)
}

func TestContentRateSubjectsAndFailurePolicy(t *testing.T) {
	const export = "/api/v1/admin/content/wishes/123/export"
	for _, test := range []struct {
		name, method, path, token, subject string
		allowed                            bool
		failed                             bool
		status                             int
	}{
		{"anonymous", "GET", "/api/v1/wishes", "", "ip:192.0.2.1", true, false, 204},
		{"invalid token uses peer", "GET", "/api/v1/wishes", "invalid", "ip:192.0.2.1", false, false, 429},
		{"account", "GET", "/api/v1/wishes", "valid", "account:student", false, false, 429},
		{"read fail open", "GET", "/api/v1/wishes", "valid", "account:student", false, true, 204},
		{"export fail closed", "POST", export, "valid", "account:admin", false, true, 503},
		{"unrelated route", "GET", "/api/v1/me/wishes", "", "", false, false, 204},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter := testContentCounter(func(_ context.Context, key string) (bool, error) {
				if key != test.subject {
					t.Fatalf("限流主体为 %q，期望 %q", key, test.subject)
				}
				if test.failed {
					return false, errors.New("Redis 不可用")
				}
				return test.allowed, nil
			})
			router := gin.New()
			if err := router.SetTrustedProxies(nil); err != nil {
				t.Fatal(err)
			}
			router.Use(ContentRate(counter, counter, func(_ context.Context, token string, admin bool) (string, error) {
				if token != "valid" {
					return "", nil
				}
				if admin {
					return "admin", nil
				}
				return "student", nil
			}, time.Minute))
			handler := func(c *gin.Context) { c.Status(http.StatusNoContent) }
			router.GET("/api/v1/wishes", handler)
			router.POST("/api/v1/admin/content/wishes/:id/export", handler)
			router.GET("/api/v1/me/wishes", handler)
			req := httptest.NewRequest(test.method, test.path, nil)
			req.Header.Set("X-Forwarded-For", "198.51.100.1")
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Fatalf("状态为 %d，期望 %d", rec.Code, test.status)
			}
			if test.status == 429 && rec.Header().Get("Retry-After") != "60" {
				t.Fatalf("缺少限流重试时间：%v", rec.Header())
			}
		})
	}
}
