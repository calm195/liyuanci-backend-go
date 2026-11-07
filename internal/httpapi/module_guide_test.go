package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moduleguide"
)

type guideStub struct{ calls int }

func (s *guideStub) ReadAdmin(context.Context, string, string) (moduleguide.AdminGuide, error) {
	s.calls++
	return moduleguide.AdminGuide{Version: 1, Modules: []moduleguide.Module{}, PublishedModules: []moduleguide.Module{}}, nil
}
func (s *guideStub) Read(context.Context, string, string) (moduleguide.Guide, error) {
	s.calls++
	return moduleguide.Guide{Modules: []moduleguide.Module{}}, nil
}
func (s *guideStub) Save(context.Context, string, string, string, moduleguide.SaveInput) (moduleguide.AdminGuide, error) {
	s.calls++
	return moduleguide.AdminGuide{}, command.ErrVersionConflict
}
func (s *guideStub) Publish(context.Context, string, string, string, moduleguide.PublishInput) (moduleguide.AdminGuide, error) {
	s.calls++
	return moduleguide.AdminGuide{}, command.ErrForbidden
}
func (s *guideStub) Acknowledge(context.Context, string, string, string, moduleguide.AckInput) (moduleguide.Guide, error) {
	s.calls++
	return moduleguide.Guide{Version: 2, AcknowledgedVersion: 2, Modules: []moduleguide.Module{}}, nil
}

func TestModuleGuideHTTP(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body, token, key string
		status, calls                        int
	}{
		{name: "管理读取", method: "GET", path: "/api/v1/admin/module-guide", token: "synthetic", status: 200, calls: 1},
		{name: "本人读取", method: "GET", path: "/api/v1/me/module-guide", token: "synthetic", status: 200, calls: 1},
		{name: "缺少会话", method: "GET", path: "/api/v1/me/module-guide", status: 401},
		{name: "拒绝查询参数", method: "GET", path: "/api/v1/me/module-guide?account_id=other", token: "synthetic", status: 400},
		{name: "拒绝未知字段", method: "PUT", path: "/api/v1/me/module-guide/ack", token: "synthetic", key: "synthetic-key", body: `{"version":2,"account_id":"other"}`, status: 400},
		{name: "拒绝无幂等键", method: "PUT", path: "/api/v1/me/module-guide/ack", token: "synthetic", body: `{"version":2}`, status: 400},
		{name: "本人确认", method: "PUT", path: "/api/v1/me/module-guide/ack", token: "synthetic", key: "synthetic-key", body: `{"version":2}`, status: 200, calls: 1},
		{name: "保存版本冲突", method: "PUT", path: "/api/v1/admin/module-guide", token: "synthetic", key: "synthetic-key", body: `{"expected_version":1,"modules":[]}`, status: 409, calls: 1},
		{name: "发布权限错误", method: "POST", path: "/api/v1/admin/module-guide/publish", token: "synthetic", key: "synthetic-key", body: `{"expected_version":1}`, status: 403, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := New(zap.NewNop(), nil)
			stub := &guideStub{}
			if err := RegisterModuleGuide(r, stub); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			if test.key != "" {
				req.Header.Set("Idempotency-Key", test.key)
			}
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, req)
			if response.Code != test.status || stub.calls != test.calls {
				t.Fatalf("状态=%d，调用=%d，响应=%s", response.Code, stub.calls, response.Body.String())
			}
		})
	}
}
