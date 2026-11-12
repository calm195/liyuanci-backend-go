package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// WishComments 是心愿评论和作者补充的独立接口边界。
type WishComments interface {
	SaveComment(context.Context, string, string, string, string, string, wish.CommentInput) (wish.Comment, error)
	WithdrawComment(context.Context, string, string, string, string, int64) (wish.Comment, error)
	GetComment(context.Context, string, string, string) (wish.Comment, error)
	ListComments(context.Context, string, string, string, bool, wish.CommentQuery) (wish.CommentPage, error)
	SetComments(context.Context, string, string, string, string, int64, bool) (wish.CommentSettings, error)
}

// RegisterWishComments 注册当前心愿上下文中的公开及本人评论接口。
func RegisterWishComments(r *gin.Engine, s WishComments) {
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	for _, path := range []string{"/wishes/:id/comments", "/me/wish-comments"} {
		route := path
		g.GET(route, func(c *gin.Context) {
			own := route == "/me/wish-comments"
			token, e := whisperBearer(c, own)
			if e != nil {
				adminError(c, e)
				return
			}
			q := wish.CommentQuery{WishID: c.Query("wish_id"), RootCommentID: c.Query("root_comment_id"), Kind: c.Query("kind"), Cursor: c.Query("cursor"), Limit: 20}
			for k, v := range c.Request.URL.Query() {
				if len(v) != 1 || (k != "kind" && k != "cursor" && k != "limit" && k != "root_comment_id" && k != "wish_id") {
					returnInvalid(c)
					return
				}
				if k == "limit" {
					q.Limit, e = strconv.Atoi(v[0])
					if e != nil || strconv.Itoa(q.Limit) != v[0] {
						returnInvalid(c)
						return
					}
				}
			}
			out, e := s.ListComments(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), own, q)
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, http.StatusOK, out)
		})
	}
	g.GET("/me/wish-comments/:id", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := s.GetComment(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	save := func(update bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			key, ok := idempotency(c)
			if !ok {
				return
			}
			var in wish.CommentInput
			data, fields, e := readWishJSON(c, []string{"body", "kind", "expected_version", "expected_post_version", "expected_comments_epoch", "root_comment_id", "reply_to_comment_id"})
			defer clear(data)
			if e != nil || fields["body"] == nil || fields["expected_post_version"] == nil || fields["expected_comments_epoch"] == nil || update && fields["expected_version"] == nil || !update && fields["expected_version"] != nil || json.Unmarshal(data, &in) != nil || !strictWhisperUnicode(data) {
				returnInvalid(c)
				return
			}
			wishID, id := c.Param("id"), ""
			if update {
				wishID, id = "", wishID
			}
			out, e := s.SaveComment(c.Request.Context(), token, key, c.GetString("request_id"), wishID, id, in)
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, http.StatusAccepted, out)
		}
	}
	g.POST("/wishes/:id/comments", save(false))
	g.PUT("/me/wish-comments/:id", save(true))
	g.POST("/me/wish-comments/:id/withdraw", func(c *gin.Context) {
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
		if !interactionJSON(c, []string{"expected_version"}, &in) {
			returnInvalid(c)
			return
		}
		out, e := s.WithdrawComment(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in.Version)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	g.PUT("/me/wishes/:id/comments-setting", func(c *gin.Context) {
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
			Open    *bool `json:"comments_open"`
		}
		if !interactionJSON(c, []string{"expected_version", "comments_open"}, &in) || in.Open == nil {
			returnInvalid(c)
			return
		}
		out, e := s.SetComments(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in.Version, *in.Open)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
}
