package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/contentinteraction"
)

// ContentInteractions 是跨内容收藏和浏览的显式边界。
type ContentInteractions interface {
	SetFavorite(context.Context, string, string, string, string, string, bool) (contentinteraction.FavoriteState, error)
	Favorites(context.Context, string, string, string, string, int) (contentinteraction.FavoritePage, error)
	View(context.Context, string, string, string, string, string) (contentquery.Stats, error)
}

// RegisterContentInteractions 注册不复制正文权限的收藏和浏览端点。
func RegisterContentInteractions(r *gin.Engine, s ContentInteractions) {
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		active := method == http.MethodPut
		g.Handle(method, "/me/favorites/:kind/:id", func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			key, ok := idempotency(c)
			if !ok {
				return
			}
			if len(c.Request.URL.Query()) > 0 {
				returnInvalid(c)
				return
			}
			out, e := s.SetFavorite(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("kind"), c.Param("id"), active)
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, http.StatusOK, out)
		})
	}
	g.GET("/me/favorites", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		limit := 20
		for k, v := range c.Request.URL.Query() {
			if len(v) != 1 || (k != "kind" && k != "cursor" && k != "limit") {
				returnInvalid(c)
				return
			}
			if k == "limit" {
				var e error
				limit, e = strconv.Atoi(v[0])
				if e != nil || strconv.Itoa(limit) != v[0] {
					returnInvalid(c)
					return
				}
			}
		}
		out, e := s.Favorites(c.Request.Context(), token, c.GetString("request_id"), c.Query("kind"), c.Query("cursor"), limit)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	g.POST("/content/:kind/:id/views", func(c *gin.Context) {
		token, e := whisperBearer(c, false)
		if e != nil {
			adminError(c, e)
			return
		}
		var in struct {
			EventID string `json:"event_id"`
		}
		if !interactionJSON(c, []string{"event_id"}, &in) {
			returnInvalid(c)
			return
		}
		out, e := s.View(c.Request.Context(), token, c.GetString("request_id"), c.Param("kind"), c.Param("id"), in.EventID)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
}

// RegisterNotificationSummary 在现有消息中心旁提供权威未读汇总。
func RegisterNotificationSummary(r *gin.Engine, read func(context.Context, string, string) (chat.NotificationSummary, error)) {
	r.GET("/api/v1/me/notifications/summary", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := bearer(c)
		if !ok {
			return
		}
		if len(c.Request.URL.Query()) > 0 {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := read(c.Request.Context(), token, c.GetString("request_id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
}
