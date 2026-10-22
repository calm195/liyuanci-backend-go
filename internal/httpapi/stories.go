package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// StoriesAPI 显式提供完成、故事及分享接口，不接受客户端指定业务或审核状态。
type StoriesAPI interface {
	Complete(context.Context, string, string, string, string, int64) (wish.CreatedDraft, error)
	SubmitStory(context.Context, string, string, string, string, wish.StoryInput) (wish.StoryAccepted, error)
	UpdateStory(context.Context, string, string, string, string, wish.StoryUpdate) (wish.StoryAccepted, error)
	WithdrawStory(context.Context, string, string, string, string, int64) (wish.CreatedDraft, error)
	Stories(context.Context, string, string, int) (wish.PublicPage, error)
	Story(context.Context, string, string, string) (wish.PublicContent, error)
	CreateShare(context.Context, string, string, string, wish.ShareInput) (wish.ShareLink, error)
	ResolveShare(context.Context, string, string, string) (wish.ShareTarget, error)
}

// RegisterStories 仅在应用明确启用故事技术预算后装配八项正式接口。
func RegisterStories(r *gin.Engine, s StoriesAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	for _, path := range []string{"/wishes/:id/complete", "/wishes/:id/story/withdraw"} {
		route := path
		g.POST(route, func(c *gin.Context) {
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
			var out wish.CreatedDraft
			var e error
			if route == "/wishes/:id/complete" {
				out, e = s.Complete(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in.Version)
			} else {
				out, e = s.WithdrawStory(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in.Version)
			}
			if e != nil {
				adminError(c, e)
				return
			}
			wishMutationResult(c, http.StatusOK, out)
		})
	}
	g.POST("/wishes/:id/stories", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in wish.StoryInput
		if !interactionJSON(c, []string{"body", "wish_expected_version"}, &in) || in.WishExpectedVersion < 1 {
			returnInvalid(c)
			return
		}
		out, e := s.SubmitStory(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	g.PUT("/wishes/:id/story", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in wish.StoryUpdate
		if !interactionJSON(c, []string{"body", "expected_version"}, &in) || in.ExpectedVersion < 1 {
			returnInvalid(c)
			return
		}
		out, e := s.UpdateStory(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	g.GET("/stories", func(c *gin.Context) {
		q, e := chatQuery(c, false)
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := s.Stories(c.Request.Context(), c.GetString("request_id"), q.Cursor, q.Limit)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	g.GET("/stories/:id", func(c *gin.Context) {
		token, ok := optionalStoryToken(c)
		if !ok {
			return
		}
		out, e := s.Story(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	g.POST("/share-links", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in wish.ShareInput
		if !interactionJSON(c, []string{"target_kind", "id"}, &in) {
			returnInvalid(c)
			return
		}
		out, e := s.CreateShare(c.Request.Context(), token, key, c.GetString("request_id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusCreated, out)
	})
	g.GET("/share-links/:ref", func(c *gin.Context) {
		token, ok := optionalStoryToken(c)
		if !ok {
			return
		}
		out, e := s.ResolveShare(c.Request.Context(), token, c.GetString("request_id"), c.Param("ref"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}

func optionalStoryToken(c *gin.Context) (string, bool) {
	if len(c.Request.Header.Values("Authorization")) == 0 {
		return "", true
	}
	return bearer(c)
}
