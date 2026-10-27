package httpapi

import (
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/safety"
)

// TestBlocksQueryValidation 覆盖REQ-014/018：无效分页在进入数据库之前拒绝，失败同样禁用缓存并记录完成日志。
func TestBlocksQueryValidation(t *testing.T) {
	for _, suffix := range []string{"?limit=0", "?limit=51", "?limit=1&limit=2", "?cursor=", "?target_user_id=forged"} {
		t.Run(suffix, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			r := New(zap.New(core), nil)
			if e := RegisterSafety(r, &safety.Service{}, &safety.Service{}); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest("GET", "/api/v1/me/blocks"+suffix, nil)
			req.Header.Set("Authorization", "Bearer synthetic")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != 400 || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(rr.Code, rr.Body.String())
			}
			if logs.FilterMessage("HTTP请求处理完成").Len() != 1 {
				t.Fatal("缺少拒绝完成日志")
			}
		})
	}
}
