package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// RegisterContentHot 装配统一周榜入口，按类别复用现有的公开投影与错误协议。
func RegisterContentHot(r *gin.Engine,
	wishes func(context.Context, string) (wish.PublicPage, error),
	whispers func(context.Context, string, string) (whisper.Page[whisper.PublicPost], error),
) error {
	if r == nil || wishes == nil || whispers == nil {
		return command.ErrInvalid
	}
	r.GET("/api/v1/content/hot", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		query := c.Request.URL.Query()
		if len(query) != 1 || len(query["kind"]) != 1 {
			adminError(c, command.ErrInvalid)
			return
		}
		ctx, rid := c.Request.Context(), c.GetString("request_id")
		switch query["kind"][0] {
		case "wish":
			out, e := wishes(ctx, rid)
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, http.StatusOK, gin.H{"items": out.Items})
		case "whisper":
			token, e := whisperBearer(c, false)
			if e != nil {
				whisperError(c, e)
				return
			}
			out, e := whispers(ctx, token, rid)
			if e != nil {
				whisperError(c, e)
				return
			}
			adminResult(c, http.StatusOK, gin.H{"items": out.Items})
		default:
			adminError(c, command.ErrInvalid)
		}
	})
	return nil
}
