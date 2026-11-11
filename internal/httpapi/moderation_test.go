package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
)

type moderationStub struct {
	ModerationService
	creates          int
	builtinOverrides int
}

func (s *moderationStub) CreateWordlist(context.Context, string, string, string, moderation.WordlistInput) (moderation.Wordlist, error) {
	s.creates++
	return moderation.Wordlist{ID: "synthetic-rule", Version: 1, Kind: "school", Status: "draft"}, nil
}

func (s *moderationStub) CreateBuiltinWordlist(_ context.Context, _, _, _ string, input moderation.BuiltinWordlistInput) (moderation.Wordlist, error) {
	s.builtinOverrides = len(input.Overrides)
	return moderation.Wordlist{ID: "synthetic-builtin", Version: 1, Kind: "builtin", Status: "draft"}, nil
}

func TestBuiltinWordlistBulkDraftBody(t *testing.T) {
	const count = 16000
	var body strings.Builder
	body.WriteString(`{"expected_version":1,"overrides":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"id":"00000000-0000-4000-8000-%012d","enabled":false,"disposition":"hint"}`, i)
	}
	body.WriteString(`]}`)
	if body.Len() <= 1<<20 {
		t.Fatal("批量测试正文未达到通用请求上限")
	}
	stub := &moderationStub{}
	router := New(zap.NewNop(), nil)
	if err := RegisterModeration(router, stub); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/admin/wordlists/builtin/versions", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Idempotency-Key", "key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 201 || stub.builtinOverrides != count {
		t.Fatalf("批量草稿状态 %d，收到 %d 条词条调整", rec.Code, stub.builtinOverrides)
	}
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
		{
			"正常创建",
			"POST",
			"/api/v1/admin/wordlists/versions",
			`{"kind":"school","entries":["停用示例"],"disabled_entries":["停用示例"],"expected_version":1}`,
			"token",
			"key",
			201,
		},
		{"缺令牌", "POST", "/api/v1/admin/wordlists/versions", `{}`, "", "key", 401},
		{"缺幂等", "POST", "/api/v1/admin/wordlists/versions", `{}`, "token", "", 400},
		{
			"重复键",
			"POST",
			"/api/v1/admin/wordlists/versions",
			`{"kind":"school","kind":"crisis"}`,
			"token",
			"key",
			400,
		},
		{
			"非法UTF8",
			"POST",
			"/api/v1/admin/wordlists/versions",
			string([]byte{123, 34, 107, 105, 110, 100, 34, 58, 34, 255, 34, 125}),
			"token",
			"key",
			400,
		},
		{"字段大小写", "POST", "/api/v1/admin/wordlists/versions", `{"Kind":"school"}`, "token", "key", 400},
		{"未知字段", "POST", "/api/v1/admin/wordlists/versions", `{"target":"forged"}`, "token", "key", 400},
		{"显式空", "POST", "/api/v1/admin/wordlists/versions", `{"entries":null}`, "token", "key", 400},
		{"尾随正文", "POST", "/api/v1/admin/wordlists/versions", `{} {}`, "token", "key", 400},
		{
			"超大正文",
			"POST",
			"/api/v1/admin/wordlists/versions",
			`{"kind":"` + strings.Repeat("x", 1<<20) + `"}`,
			"token",
			"key",
			400,
		},
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

func (s *moderationStub) QueryCases(_ context.Context, _, _ string, input moderation.CaseQuery) (moderation.Page, error) {
	return moderation.Page{Items: []moderation.Summary{{ID: input.Q, Status: input.Status, Kind: input.Kind}}}, nil
}

// TestModerationCasesQueryHTTP 验证新筛选严格解析、原接口路径与默认分页。
func TestModerationCasesQueryHTTP(t *testing.T) {
	router := New(zap.NewNop(), nil)
	if err := RegisterModeration(router, &moderationStub{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"?q=abc&status=all&kind=wish&limit=1", 200},
		{"", 200}, {"?q=a&q=b", 400}, {"?status=all&status=approved", 400}, {"?unknown=x", 400},
		{"?limit=0", 400}, {"?limit=101", 400}, {"?cursor=", 400}, {"?q=%GG", 400},
	} {
		req := httptest.NewRequest("GET", "/api/v1/admin/moderation-tasks"+tc.query, nil)
		req.Header.Set("Authorization", "Bearer token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, rec.Code, rec.Body.String())
		}
	}
}
