package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

type profileFixture struct {
	called                bool
	token, target, cursor string
	limit                 int
	failure               error
	panicRead             bool
}

func (f *profileFixture) PublicProfile(_ context.Context, token, _, target, cursor string, limit int) (wish.ProfileContents, error) {
	f.called, f.token, f.target, f.cursor, f.limit = true, token, target, cursor, limit
	if f.panicRead {
		panic("内部凭据不可暴露")
	}
	return wish.ProfileContents{Profile: wish.ProfileSummary{ID: target, Nickname: "合成公开昵称"}, Items: []wish.ProfileItem{}}, f.failure
}

// TestPublicProfileHTTP 覆盖REQ-008/018的公开读取、参数边界、错误映射和失败完成日志。
func TestPublicProfileHTTP(t *testing.T) {
	const id = "60000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name, suffix, authorization string
		failure                     error
		status                      int
		called, panicRead           bool
	}{
		{name: "游客", status: 200, called: true},
		{name: "会话", suffix: "?limit=50&cursor=signed", authorization: "Bearer session", status: 200, called: true},
		{name: "无效认证头", authorization: "Basic invalid", status: 401},
		{name: "过大分页", suffix: "?limit=51", status: 400},
		{name: "零分页", suffix: "?limit=0", status: 400},
		{name: "重复参数", suffix: "?limit=1&limit=2", status: 400},
		{name: "空游标", suffix: "?cursor=", status: 400},
		{name: "未知参数", suffix: "?state=draft", status: 400},
		{name: "会话失效", authorization: "Bearer expired", failure: command.ErrUnauthenticated, status: 401, called: true},
		{name: "目标不可见", failure: command.ErrNotFound, status: 404, called: true},
		{name: "依赖失败", failure: command.ErrUnavailable, status: 503, called: true},
		{name: "异常恢复", status: 500, called: true, panicRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			r := New(zap.New(core), func(context.Context) error { return nil })
			f := &profileFixture{failure: tc.failure, panicRead: tc.panicRead}
			if e := RegisterPublicProfile(r, f); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest("GET", "/api/v1/users/"+id+"/public-profile"+tc.suffix, nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.status || f.called != tc.called {
				t.Fatal(rr.Code, rr.Body.String(), f.called)
			}
			if rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("X-Request-ID") == "" {
				t.Fatal("缺少缓存或请求标识边界")
			}
			if strings.Contains(rr.Body.String(), "内部凭据") {
				t.Fatal("泄漏内部错误")
			}
			if tc.status == 200 && (f.target != id || f.limit < 1 || !strings.Contains(rr.Body.String(), `"items":[]`) || !strings.Contains(rr.Body.String(), `"next_cursor":null`)) {
				t.Fatal(f, rr.Body.String())
			}
			if tc.name == "会话" && (f.token != "session" || f.cursor != "signed" || f.limit != 50) {
				t.Fatal("参数传递丢失")
			}
			if logs.Len() == 0 {
				t.Fatal("缺少请求完成日志")
			}
		})
	}
	r := New(zap.NewNop(), func(context.Context) error { return nil })
	f := &profileFixture{}
	if RegisterPublicProfile(nil, f) == nil || RegisterPublicProfile(r, nil) == nil {
		t.Fatal("缺依赖仍注册")
	}
	if e := RegisterPublicProfile(r, f); e != nil {
		t.Fatal(e)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/users/invalid/public-profile", nil))
	if rr.Code != 400 || f.called {
		t.Fatal("非法目标进入领域查询")
	}
}
