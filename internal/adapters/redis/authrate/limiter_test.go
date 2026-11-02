package authrate

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// TestRequiredSharedLimiter 验证缺共享缓存、无效窗口和限额均失败关闭，不创建本地替代限流器。
func TestRequiredSharedLimiter(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer client.Close()
	for _, tc := range []struct {
		name   string
		client redis.UniversalClient
		window time.Duration
		limit  int
	}{
		{"缺缓存", nil, time.Minute, 1},
		{"窗口不足", client, time.Microsecond, 1},
		{"无效限额", client, time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if limiter, err := New(tc.client, tc.window, tc.limit); err != command.ErrInvalid || limiter != nil {
				t.Fatal("限流缺配置仍可用", err)
			}
		})
	}
}
