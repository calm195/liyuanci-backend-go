package httpapi

import (
	"context"
	"io"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
)

// AdminDashboardAPI 是只返回汇总统计的后台入口。
type AdminDashboardAPI interface {
	ReadDashboard(context.Context, string, string, string) (adminauth.Dashboard, error)
}

// RegisterAdminDashboard 显式装配运营统计，严格限制时间窗口参数。
func RegisterAdminDashboard(r *gin.Engine, service AdminDashboardAPI) error {
	if r == nil || service == nil {
		return adminauth.ErrInvalid
	}
	r.GET("/api/v1/admin/dashboard", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, err := url.ParseQuery(c.Request.URL.RawQuery)
		period := q.Get("range")
		if period == "" {
			period = "month"
		}
		body, readErr := io.ReadAll(io.LimitReader(c.Request.Body, 1))
		if err != nil || readErr != nil || len(body) > 0 || len(q) > 1 || (len(q) > 0 && len(q["range"]) != 1) || (period != "month" && period != "year") {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		out, err := service.ReadDashboard(c.Request.Context(), token, c.GetString("request_id"), period)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, 200, out)
	})
	return nil
}
