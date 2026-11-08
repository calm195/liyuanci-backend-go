package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/dictionary"
)

type dictionaryStub struct{ calls int }

func (s *dictionaryStub) Read(context.Context, string) (dictionary.Snapshot, error) {
	s.calls++
	return dictionary.Snapshot{Version: 1, Items: []dictionary.Item{}}, nil
}

func (s *dictionaryStub) ReadAdmin(context.Context, string, string) (dictionary.Snapshot, error) {
	s.calls++
	return dictionary.Snapshot{}, nil
}

func (s *dictionaryStub) Save(context.Context, string, string, string, string, string, dictionary.SaveInput) (dictionary.Snapshot, error) {
	s.calls++
	return dictionary.Snapshot{}, command.ErrVersionConflict
}

func (s *dictionaryStub) ReadReportTypes(context.Context, string, string) (dictionary.ReportTypesSnapshot, error) {
	s.calls++
	return dictionary.ReportTypesSnapshot{}, nil
}

func (s *dictionaryStub) SaveReportType(context.Context, string, string, string, string, dictionary.SaveReportTypeInput) (dictionary.ReportTypesSnapshot, error) {
	s.calls++
	return dictionary.ReportTypesSnapshot{}, command.ErrForbidden
}

func TestDictionariesHTTP(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body, token, key string
		status, calls                        int
	}{
		{name: "公开展示无需会话", method: "GET", path: "/api/v1/dictionaries", status: 200, calls: 1},
		{name: "管理读取需要会话", method: "GET", path: "/api/v1/admin/dictionaries", status: 401},
		{name: "拒绝未知查询", method: "GET", path: "/api/v1/dictionaries?account_id=other", status: 400},
		{name: "拒绝无幂等键", method: "PUT", path: "/api/v1/admin/dictionaries/resource_feedback/valid", token: "synthetic", body: `{"expected_version":1,"label":"有帮助","sort_order":0,"enabled":true}`, status: 400},
		{name: "拒绝未知字段", method: "PUT", path: "/api/v1/admin/dictionaries/resource_feedback/valid", token: "synthetic", key: "test-key", body: `{"expected_version":1,"label":"有帮助","sort_order":0,"enabled":true,"role":"super_admin"}`, status: 400},
		{name: "版本冲突映射", method: "PUT", path: "/api/v1/admin/dictionaries/resource_feedback/valid", token: "synthetic", key: "test-key", body: `{"expected_version":1,"label":"有帮助","sort_order":0,"enabled":true}`, status: 409, calls: 1},
		{name: "读取举报运营字典", method: "GET", path: "/api/v1/admin/report-types", token: "synthetic", status: 200, calls: 1},
		{name: "举报修改权限拒绝", method: "PUT", path: "/api/v1/admin/report-types/other", token: "synthetic", key: "test-key", body: `{"expected_version":1,"label":"其他","published":true}`, status: 403, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := New(zap.NewNop(), nil)
			stub := &dictionaryStub{}
			if err := RegisterDictionaries(router, stub); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			if test.key != "" {
				req.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != test.status || stub.calls != test.calls {
				t.Fatalf("状态=%d,调用=%d,响应=%s", response.Code, stub.calls, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("字典响应缓存策略缺失")
			}
		})
	}
}
