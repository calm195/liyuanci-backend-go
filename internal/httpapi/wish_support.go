package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// RegisterWishSupport 注册显式支持和取消入口，使断线重试保持同一操作意图。
func RegisterWishSupport(r *gin.Engine, set func(context.Context, string, string, string, bool) (wish.Support, error)) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		r.Handle(method, "/api/v1/wishes/:id/support", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			token, ok := bearer(c)
			if !ok {
				return
			}
			out, err := set(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), method == http.MethodPut)
			if err != nil {
				adminError(c, err)
				return
			}
			adminResult(c, http.StatusOK, out)
		})
	}
}
