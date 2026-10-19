package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Readiness 检查依赖是否具备处理请求的条件。
type Readiness func(context.Context) error

// New 构建包含日志、异常恢复和健康检查端点的 HTTP 路由。
func New(log *zap.Logger, ready Readiness) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(func(c *gin.Context) {
		start := time.Now()
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		requestID := hex.EncodeToString(id[:])
		c.Header("X-Request-ID", requestID)
		c.Set("request_id", requestID)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		// 外层中间件在异常恢复和处理函数结束后记录最终状态。
		c.Next()
		log.Info("http request completed",
			// T004/REQ-020：告警按最终status而非日志级别判断HTTP失败。
			zap.String("event", "http_request_completed"),
			zap.String("request_id", requestID),
			zap.String("method", c.Request.Method),
			zap.String("path", c.FullPath()),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("duration", time.Since(start)),
		)
	}, func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				// panic 值可能包含敏感信息，因此只保留请求关联信息，不记录其内容。
				log.Error("request panic", zap.String("event", "http_panic"), zap.String("request_id", c.GetString("request_id")))
				c.AbortWithStatusJSON(500, gin.H{"code": 500, "message": "服务内部错误", "error_code": "INTERNAL_ERROR", "request_id": c.GetString("request_id")})
			}
		}()
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			c.JSON(503, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	})
	// T004/REQ-018：尚未实现的业务入口不返回模拟成功，也不暴露模板示例。
	r.NoRoute(func(c *gin.Context) {
		c.JSON(404, gin.H{"code": 404, "message": "资源不可用", "error_code": "NOT_FOUND", "request_id": c.GetString("request_id")})
	})
	return r
}
