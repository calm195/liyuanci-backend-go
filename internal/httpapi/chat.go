package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// ChatAPI 显式暴露聊天与站内通知，不提供消息枚举、编辑或删除能力。
type ChatAPI interface {
	Conversations(context.Context, string, string, chat.Query) (chat.ConversationsPage, error)
	Messages(context.Context, string, string, string, chat.Query) (chat.MessagesPage, error)
	Send(context.Context, string, string, string, string, chat.MessageInput) (chat.Accepted, error)
	MessageStatus(context.Context, string, string, string) (chat.Status, error)
	Notifications(context.Context, string, string, chat.Query) (chat.NotificationsPage, error)
	ReadNotification(context.Context, string, string, string, string, int64) (chat.Notification, error)
}

// RegisterChat 注册正式六项接口；所有入口禁止缓存。
func RegisterChat(r *gin.Engine, s ChatAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	for _, path := range []string{"/me/conversations", "/conversations/:id/messages", "/me/notifications"} {
		route := path
		g.GET(route, func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			q, e := chatQuery(c, route == "/conversations/:id/messages")
			if e != nil {
				adminError(c, e)
				return
			}
			var out any
			switch route {
			case "/me/conversations":
				out, e = s.Conversations(c.Request.Context(), token, c.GetString("request_id"), q)
			case "/me/notifications":
				out, e = s.Notifications(c.Request.Context(), token, c.GetString("request_id"), q)
			default:
				out, e = s.Messages(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), q)
			}
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, http.StatusOK, out)
		})
	}
	g.POST("/conversations/:id/messages", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in chat.MessageInput
		if !interactionJSON(c, []string{"client_message_id", "body", "expected_write_epoch"}, &in) || in.ExpectedWriteEpoch < 0 {
			returnInvalid(c)
			return
		}
		out, e := s.Send(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	g.GET("/me/messages/:id/status", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := s.MessageStatus(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	g.POST("/me/notifications/:id/read", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in struct {
			Version int64 `json:"expected_version"`
		}
		if !interactionJSON(c, []string{"expected_version"}, &in) || in.Version < 1 {
			returnInvalid(c)
			return
		}
		out, e := s.ReadNotification(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in.Version)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}
func chatQuery(c *gin.Context, messages bool) (q chat.Query, err error) {
	q.Limit = 20
	allowed := []string{"cursor", "limit"}
	if messages {
		q.Limit = 50
		allowed = append(allowed, "after_seq")
	}
	for name, values := range c.Request.URL.Query() {
		if !slices.Contains(allowed, name) || len(values) != 1 || values[0] == "" {
			return q, command.ErrInvalid
		}
	}
	q.Cursor = c.Query("cursor")
	if value, ok := c.GetQuery("limit"); ok {
		q.Limit, err = strconv.Atoi(value)
		if err != nil || q.Limit < 1 || q.Limit > 50 {
			return q, command.ErrInvalid
		}
	}
	if value, ok := c.GetQuery("after_seq"); ok {
		seq, e := strconv.ParseInt(value, 10, 64)
		if e != nil || seq < 0 || q.Cursor != "" {
			return q, command.ErrInvalid
		}
		q.AfterSeq = &seq
	}
	return q, nil
}
