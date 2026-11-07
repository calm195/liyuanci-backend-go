package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
)

type dashboardFixture struct {
	calls   int
	period  string
	failure error
}

func (f *dashboardFixture) ReadDashboard(_ context.Context, _, _, period string) (adminauth.Dashboard, error) {
	f.calls++
	f.period = period
	return adminauth.Dashboard{Range: period, Series: []adminauth.DashboardPoint{}}, f.failure
}

// TestDashboardHTTP 验证未认证与非法参数不能触达统计服务，授权失败不泄露结果。
func TestDashboardHTTP(t *testing.T) {
	for _, tc := range []struct {
		query, token string
		failure      error
		status       int
		calls        int
	}{
		{"", "", nil, 401, 0},
		{"", "test-token", nil, 200, 1},
		{"?range=year", "test-token", nil, 200, 1},
		{"?range=week", "test-token", nil, 400, 0},
		{"?range=month&range=year", "test-token", nil, 400, 0},
		{"?extra=1", "test-token", nil, 400, 0},
		{"?range=%zz", "test-token", nil, 400, 0},
		{"", "test-token", adminauth.ErrForbidden, 403, 1},
		{"", "test-token", errors.New("内部错误"), 503, 1},
	} {
		f := &dashboardFixture{failure: tc.failure}
		r := New(zap.NewNop(), func(context.Context) error { return nil })
		if err := RegisterAdminDashboard(r, f); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/api/v1/admin/dashboard"+tc.query, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != tc.status || f.calls != tc.calls || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("统计边界失败 %q：状态%d 调用%d", tc.query, rec.Code, f.calls)
		}
	}
}
