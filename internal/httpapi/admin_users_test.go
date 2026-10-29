package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
)

type usersStub struct {
	*safety.Service
	called  bool
	q       safety.UsersQuery
	failure error
}

func (s *usersStub) UsersList(_ context.Context, _, _ string, q safety.UsersQuery) (safety.UserPage, error) {
	s.called = true
	s.q = q
	return safety.UserPage{Items: []safety.UserSummary{}}, s.failure
}
func (s *usersStub) UserDetailRead(context.Context, string, string, string) (safety.UserDetail, error) {
	s.called = true
	return safety.UserDetail{}, s.failure
}
func (s *usersStub) UserPenalties(_ context.Context, _, _, _ string, q safety.UsersQuery) (safety.UserPenaltyPage, error) {
	s.called = true
	s.q = q
	return safety.UserPenaltyPage{Items: []safety.UserPenalty{}}, s.failure
}

// TestAdminUsersHTTP 拒绝重复/未知/非法参数，保持标准响应头、错误映射及查询串脱敏。
func TestAdminUsersHTTP(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		path    string
		status  int
		failure error
	}{
		{"", 200, nil}, {"?nickname=", 200, nil}, {"?nickname=%E3%80%80LiFish%25_", 200, nil},
		{"?limit=", 400, nil}, {"?cursor=", 400, nil}, {"?user_id=", 400, nil}, {"?limit=0", 400, nil}, {"?limit=51", 400, nil}, {"?limit=%2B1", 400, nil},
		{"?limit=1&limit=1", 400, nil}, {"?nickname=a&nickname=a", 400, nil}, {"?unknown=x", 400, nil}, {"?verification=all", 400, nil}, {"?nickname=%zz", 400, nil}, {"?a;b=c", 400, nil},
		{"?nickname=" + url.QueryEscape(strings.Repeat("鲤", 51)), 400, nil},
		{"/invalid", 400, nil}, {"/" + id + "?nickname=a", 400, nil}, {"/" + id + "/penalties?nickname=", 400, nil},
		{"/" + id + "/penalties?limit=1", 200, nil}, {"", 401, command.ErrUnauthenticated}, {"", 403, command.ErrForbidden}, {"/" + id, 404, command.ErrNotFound}, {"", 500, command.ErrInternal}, {"", 503, command.ErrUnavailable},
	} {
		t.Run(tc.path+string(rune(tc.status)), func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			r := New(zap.New(core), nil)
			s := &usersStub{failure: tc.failure}
			if e := RegisterAdminUsers(r, s); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest("GET", "/api/v1/admin/users"+tc.path, nil)
			req.Header.Set("Authorization", "Bearer synthetic")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.status || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(rr.Code, rr.Body.String())
			}
			if tc.status == 400 && s.called {
				t.Fatal("非法参数进入服务")
			}
			var body map[string]any
			if e := json.Unmarshal(rr.Body.Bytes(), &body); e != nil {
				t.Fatal(e)
			}
			if body["request_id"] != rr.Header().Get("X-Request-ID") {
				t.Fatal("请求ID不一致")
			}
			for _, entry := range logs.All() {
				b, _ := json.Marshal(entry.ContextMap())
				if strings.Contains(string(b), "LiFish") || strings.Contains(string(b), "nickname=") {
					t.Fatal("HTTP日志泄漏查询值")
				}
			}
		})
	}
}
