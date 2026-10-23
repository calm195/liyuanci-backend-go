package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// CareAPI 只提供当前本人关怀资料，不暴露后台事件。
type CareAPI interface {
	Care(context.Context, string, string, string) (chat.CareCard, error)
}

// RegisterCare 显式注册本人关怀读取，卡片引用沿用现有通知target_ref。
func RegisterCare(r *gin.Engine, s CareAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	r.GET("/api/v1/me/care-cards/:id", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		token, ok := bearer(c)
		if !ok {
			return
		}
		if len(c.Request.URL.Query()) != 0 {
			returnInvalid(c)
			return
		}
		out, e := s.Care(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	return nil
}
