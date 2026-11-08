package app

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
)

// startContentWarm 异步预热并周期补齐默认公开列表和榜单候选。
func startContentWarm(ctx context.Context, log *zap.Logger, ttl time.Duration, warm func(context.Context) error) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(ttl / 2)
		defer ticker.Stop()
		for ctx.Err() == nil {
			attempt, stop := context.WithTimeout(ctx, 15*time.Second)
			err := warm(attempt)
			stop()
			if err != nil && ctx.Err() == nil {
				log.Warn("公开内容候选预热失败", zap.String("event", "content_cache_warm"))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func contentInvalidation(options *PublicContentOptions) gin.HandlerFunc {
	if options != nil {
		if cache, ok := options.Cache.(interface {
			Invalidate(context.Context, ...string)
		}); ok {
			return httpapi.ContentInvalidation(cache.Invalidate)
		}
	}
	return httpapi.ContentInvalidation(nil)
}
