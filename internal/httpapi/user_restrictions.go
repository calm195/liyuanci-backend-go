package httpapi

import (
	"context"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
)

// UserRestrictionsAPI 提供直接处置前的实时状态和版本。
type UserRestrictionsAPI interface {
	UserRestrictions(context.Context, string, string, string) (safety.UserRestrictionState, error)
}

func registerUserRestrictions(r *gin.Engine, service UserRestrictionsAPI) {
	r.GET("/api/v1/admin/users/:id/restrictions", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, e := url.ParseQuery(c.Request.URL.RawQuery)
		if e != nil || len(q) != 0 {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := service.UserRestrictions(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
}
