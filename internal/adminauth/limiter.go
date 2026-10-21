package adminauth

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLimiter 使用共享Redis原子计数，键仅为加密摘要，不存登录名或来源地址。
// 无本地缓存或故障放行，生产连接/凭据由外层资源管理。
type RedisLimiter struct {
	client redis.UniversalClient
	window time.Duration
	limit  int
}

// NewRedisLimiter 要求显式窗口/限额，避免把测试阈值变成生产策略。
func NewRedisLimiter(client redis.UniversalClient, window time.Duration, limit int) (*RedisLimiter, error) {
	if client == nil || window < time.Millisecond || limit < 1 {
		return nil, ErrInvalid
	}
	return &RedisLimiter{client: client, window: window, limit: limit}, nil
}

// Allow 把计数与首次过期写入放在同一脚本中；错误只由服务操作边界记录稳定结果。
func (l *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {
	result, err := l.client.Eval(ctx, `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end; return n`, []string{"admin-login:" + key}, l.window.Milliseconds()).Int64()
	if err != nil {
		return false, ErrUnavailable
	}
	return result <= int64(l.limit), nil
}
