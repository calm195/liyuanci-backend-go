package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ContentCounter 消费一次限流额度，Redis 不可用时返回错误。
type ContentCounter interface {
	Allow(context.Context, string) (bool, error)
}

// ContentActor 只解析限流分桶主体，实际业务授权仍由处理器执行。
type ContentActor func(context.Context, string, bool) (string, error)

// ContentRate 为公开读取和后台导出使用独立预算。公开读取在限流存储故障时可继续访问。
func ContentRate(read, export ContentCounter, actor ContentActor, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		counter, admin := contentCounter(c.Request.Method, c.FullPath(), read, export)
		if counter == nil {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 200*time.Millisecond)
		defer cancel()
		subject := "ip:" + c.ClientIP()
		if token, _ := whisperBearer(c, false); token != "" && len(token) <= 128 {
			account, err := actor(ctx, token, admin)
			if err == nil && account != "" {
				subject = "account:" + account
			}
		}
		allowed, err := counter.Allow(ctx, subject)
		if err != nil {
			if admin {
				abortContentRate(c, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "内容导出暂不可用")
			}
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(int((window+time.Second-1)/time.Second)))
			abortContentRate(c, http.StatusTooManyRequests, "RATE_LIMITED", "请求过于频繁，请稍后重试")
		}
	}
}

func contentCounter(method, path string, read, export ContentCounter) (ContentCounter, bool) {
	switch method {
	case http.MethodGet:
		switch path {
		case "/api/v1/wishes", "/api/v1/stories", "/api/v1/whispers/posts", "/api/v1/content/hot",
			"/api/v1/wishes/:id", "/api/v1/stories/:id", "/api/v1/whispers/posts/:post_id", "/api/v1/wishes/:id/comments", "/api/v1/whispers/posts/:post_id/comments", "/api/v1/me/favorites", "/api/v1/me/notifications/summary":
			return read, false
		}
	case http.MethodPost:
		switch path {
		case "/api/v1/content/:kind/:id/views":
			return read, false
		case "/api/v1/admin/content/wishes/:id/export", "/api/v1/admin/content/whispers/:id/export":
			return export, true
		}
	}
	return nil, false
}

func abortContentRate(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"code": status, "message": message, "error_code": code,
		"request_id": c.GetString("request_id"),
	})
}
