package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// PublicProfileAPI 只提供正式公开主页读取，不接受本人草稿或私有身份查询。
type PublicProfileAPI interface {
	PublicProfile(context.Context, string, string, string, string, int) (wish.ProfileContents, error)
}

// RegisterPublicProfile 由启用心愿的应用显式注册；健康模式不隐式开放业务路由。
func RegisterPublicProfile(r *gin.Engine, s PublicProfileAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	r.GET("/api/v1/users/:id/public-profile", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		token, ok := optionalStoryToken(c)
		if !ok {
			return
		}
		id := c.Param("id")
		parsed, e := uuid.Parse(id)
		if e != nil || parsed.String() != id {
			returnInvalid(c)
			return
		}
		q, e := chatQuery(c, false)
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := s.PublicProfile(c.Request.Context(), token, c.GetString("request_id"), id, q.Cursor, q.Limit)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}
