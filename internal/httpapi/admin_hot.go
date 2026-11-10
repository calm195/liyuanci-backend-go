package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

type adminHotItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	HeatScore    int64  `json:"heat_score"`
	SupportCount *int64 `json:"support_count,omitempty"`
}

// RegisterAdminHot 复用公开周榜资格与排序，仅向有效后台会话返回运营所需字段。
func RegisterAdminHot(r *gin.Engine,
	restore func(context.Context, string, string) (adminauth.Session, error),
	wishes func(context.Context, string) (wish.PublicPage, error),
	whispers func(context.Context, string) (whisper.Page[whisper.PublicPost], error),
) {
	r.GET("/api/v1/admin/content/hot", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := bearer(c)
		if !ok {
			return
		}
		query := c.Request.URL.Query()
		if len(query) != 1 || len(query["kind"]) != 1 {
			adminError(c, command.ErrInvalid)
			return
		}
		ctx, rid := c.Request.Context(), c.GetString("request_id")
		if _, err := restore(ctx, token, rid); err != nil {
			adminError(c, err)
			return
		}
		items := []adminHotItem{}
		switch query["kind"][0] {
		case "wish":
			out, err := wishes(ctx, rid)
			if err != nil {
				adminError(c, err)
				return
			}
			for _, item := range out.Items {
				items = append(items, adminHotItem{ID: item.ID, Title: item.Title, Body: item.Body, HeatScore: item.HeatScore, SupportCount: &item.SupportCount})
			}
		case "whisper":
			out, err := whispers(ctx, rid)
			if err != nil {
				whisperError(c, err)
				return
			}
			for _, item := range out.Items {
				items = append(items, adminHotItem{ID: item.ID, Title: item.Title, Body: item.Body, HeatScore: item.HeatScore})
			}
		default:
			adminError(c, command.ErrInvalid)
			return
		}
		adminResult(c, http.StatusOK, gin.H{"items": items})
	})
}
