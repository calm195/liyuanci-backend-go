// Package contentcache 缓存公开候选的标识和排序元数据，正文仍由业务服务从数据库读取。
package contentcache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
)

const cacheBudget = 150 * time.Millisecond

// Cache 借用宿主 Redis。缓存失效或访问异常时，查询按未命中处理。
type Cache struct {
	client redis.UniversalClient
	ttl    time.Duration
}

// New 创建共享候选缓存，生命周期跟随调用方。
func New(client redis.UniversalClient, ttl time.Duration) *Cache {
	return &Cache{client: client, ttl: ttl}
}

// Get 读取有界候选集合。空列表也是有效缓存。
func (c *Cache) Get(ctx context.Context, key string) ([]contentquery.Candidate, bool) {
	ctx, cancel := context.WithTimeout(ctx, cacheBudget)
	defer cancel()
	data, err := c.client.Get(ctx, "content-candidates:v1:"+key).Bytes()
	if err != nil || len(data) > 32<<10 {
		return nil, false
	}
	var candidates []contentquery.Candidate
	if json.Unmarshal(data, &candidates) != nil || len(candidates) > 100 {
		return nil, false
	}
	for _, candidate := range candidates {
		id, err := uuid.Parse(candidate.ID)
		if err != nil || id.String() != candidate.ID || candidate.At.IsZero() || candidate.Score < 0 {
			return nil, false
		}
	}
	return candidates, true
}

// Set 写入短期候选集合。缓存异常由业务查询回源处理。
func (c *Cache) Set(ctx context.Context, key string, candidates []contentquery.Candidate) {
	if len(candidates) > 100 {
		return
	}
	data, err := json.Marshal(candidates)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cacheBudget)
	defer cancel()
	_ = c.client.Set(ctx, "content-candidates:v1:"+key, data, c.ttl).Err()
}

// Invalidate 删除受业务状态变化影响的候选集合。
func (c *Cache) Invalidate(ctx context.Context, keys ...string) {
	ctx, cancel := context.WithTimeout(ctx, cacheBudget)
	defer cancel()
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, "content-candidates:v1:"+key)
	}
	_ = c.client.Del(ctx, names...).Err()
}
