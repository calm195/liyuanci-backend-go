package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
)

type crisisStub struct {
	CrisisAPI
	calls int
}

func (s *crisisStub) ActOnCrisis(context.Context, string, string, string, string, moderation.CrisisCommand, bool) (moderation.CrisisView, error) {
	s.calls++
	return moderation.CrisisView{}, command.ErrForbidden
}

// TestCrisisHTTPBoundary 验证危机写入严格字段、鉴权、幂等和敏感响应缓存，拒绝任意身份输入。
func TestCrisisHTTPBoundary(t *testing.T) {
	stub := &crisisStub{}
	r := New(zap.NewNop(), nil)
	if RegisterCrisis(r, stub) != nil {
		t.Fatal("装配失败")
	}
	for _, tc := range []struct {
		name, token, key, body string
		status                 int
	}{
		{"缺会话", "", "key", `{}`, 401},
		{"缺幂等", "token", "", `{}`, 400},
		{"重复字段", "token", "key", `{"action":"resolve","action":"follow_up","reason":"合成","expected_version":1}`, 400},
		{"未知字段", "token", "key", `{"action":"resolve","reason":"合成","expected_version":1,"student_number":"forged"}`, 400},
		{"空字段", "token", "key", `{"action":null,"reason":"合成","expected_version":1}`, 400},
		{"缺字段", "token", "key", `{"action":"resolve","expected_version":1}`, 400},
		{"尾随JSON", "token", "key", `{"action":"resolve","reason":"合成","expected_version":1} {}`, 400},
		{"非法UTF8", "token", "key", string([]byte{123, 34, 97, 34, 58, 34, 255, 34, 125}), 400},
		{"角色拒绝", "token", "key", `{"action":"resolve","reason":"合成","expected_version":1}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/admin/crisis-cases/opaque/actions", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatal(rr.Code, rr.Body.String())
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("敏感响应可缓存")
			}
		})
	}
	if stub.calls != 1 {
		t.Fatal("非法输入调用服务", stub.calls)
	}
}
