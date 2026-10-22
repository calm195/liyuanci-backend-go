package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/moderation"
)

type moderationStub struct {
	ModerationService
	creates int
}

func (s *moderationStub) CreateWordlist(context.Context, string, string, string, moderation.WordlistInput) (moderation.Wordlist, error) {
	s.creates++
	return moderation.Wordlist{ID: "synthetic-rule", Version: 1, Kind: "school", Status: "draft"}, nil
}
func (s *moderationStub) Wordlists(context.Context, string, string, string, int) (moderation.WordlistPage, error) {
	return moderation.WordlistPage{Items: []moderation.Wordlist{}}, nil
}

// TestModerationHTTPBoundary 验证 T016/REQ-015、019 的严格字段、幂等及敏感响应缓存边界。
func TestModerationHTTPBoundary(t *testing.T) {
	stub := &moderationStub{}
	router := New(zap.NewNop(), nil)
	if RegisterModeration(router, stub) != nil {
		t.Fatal("装配失败")
	}
	for _, tc := range []struct {
		name, method, path, body, token, key string
		status                               int
	}{
		{"正常创建", "POST", "/api/v1/admin/wordlists/versions", `{"kind":"school","entries":[],"expected_version":1}`, "token", "key", 201},
		{"缺令牌", "POST", "/api/v1/admin/wordlists/versions", `{}`, "", "key", 401},
		{"缺幂等", "POST", "/api/v1/admin/wordlists/versions", `{}`, "token", "", 400},
		{"重复键", "POST", "/api/v1/admin/wordlists/versions", `{"kind":"school","kind":"crisis"}`, "token", "key", 400},
		{"非法UTF8", "POST", "/api/v1/admin/wordlists/versions", string([]byte{123, 34, 107, 105, 110, 100, 34, 58, 34, 255, 34, 125}), "token", "key", 400},
		{"字段大小写", "POST", "/api/v1/admin/wordlists/versions", `{"Kind":"school"}`, "token", "key", 400},
		{"未知字段", "POST", "/api/v1/admin/wordlists/versions", `{"target":"forged"}`, "token", "key", 400},
		{"显式空", "POST", "/api/v1/admin/wordlists/versions", `{"entries":null}`, "token", "key", 400},
		{"尾随正文", "POST", "/api/v1/admin/wordlists/versions", `{} {}`, "token", "key", 400},
		{"超大正文", "POST", "/api/v1/admin/wordlists/versions", `{"kind":"` + strings.Repeat("x", 1<<20) + `"}`, "token", "key", 400},
		{"重复分页", "GET", "/api/v1/admin/wordlists?limit=1&limit=2", "", "token", "", 400},
		{"未知查询", "GET", "/api/v1/admin/wordlists?body=forged", "", "token", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("状态%d，期望%d", rec.Code, tc.status)
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
				t.Fatal("缺缓存保护或请求标识")
			}
		})
	}
	if stub.creates != 1 {
		t.Fatal("协议失败仍调用业务写入")
	}
}
