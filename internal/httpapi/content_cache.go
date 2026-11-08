package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
)

// ContentInvalidation 清理已成功变更公开状态的候选集。异步审核和自然过期仍由短 TTL
// 与读时可见性复核覆盖。
func ContentInvalidation(invalidate func(context.Context, ...string)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if invalidate == nil {
			return
		}
		keys := changedContent(c.Request.Method, c.FullPath())
		if len(keys) == 0 {
			return
		}
		c.Next()
		if c.Writer.Status() >= http.StatusOK && c.Writer.Status() < http.StatusMultipleChoices {
			// 客户端断开时，已经提交的变更仍要有机会失效缓存。
			ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 150*time.Millisecond)
			defer cancel()
			invalidate(ctx, keys...)
		}
	}
}

func changedContent(method, path string) []string {
	switch method {
	case http.MethodPost:
		switch path {
		case "/api/v1/wishes/:id/publish", "/api/v1/wishes/:id/withdraw", "/api/v1/applications/:id/accept":
			return []string{contentquery.WishListKey, contentquery.WishWeeklyKey}
		case "/api/v1/wishes/:id/story/withdraw":
			return []string{contentquery.StoryListKey}
		case "/api/v1/wishes/:id/complete", "/api/v1/wishes/:id/rollback", "/api/v1/admin/moderation-tasks/:id/decision":
			return []string{contentquery.WishListKey, contentquery.WishWeeklyKey, contentquery.StoryListKey}
		case "/api/v1/me/whispers/posts/:post_id/withdraw", "/api/v1/admin/whispers/reviews/:id/decision":
			return []string{contentquery.WhisperListKey, contentquery.WhisperWeeklyKey}
		}
	case http.MethodPut:
		switch path {
		case "/api/v1/wishes/:id/content":
			return []string{contentquery.WishListKey, contentquery.WishWeeklyKey}
		case "/api/v1/wishes/:id/story":
			return []string{contentquery.StoryListKey}
		case "/api/v1/me/whispers/posts/:post_id", "/api/v1/admin/whispers/contents/:kind/:id/restriction":
			return []string{contentquery.WhisperListKey, contentquery.WhisperWeeklyKey}
		}
	}
	return nil
}
