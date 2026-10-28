package whisperrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// TestRedisWhisperAdmission 验证真实滚动窗口、原子并发、释放与依赖失败；只操作本测试随机主体的键。
func TestRedisWhisperAdmission(t *testing.T) {
	address := os.Getenv("APP_WHISPER_REDIS_TEST_ADDRESS")
	if address == "" {
		t.Skip("需显式隔离 Redis；make integration 覆盖此用例")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	limiter, e := New(client, bytes.Repeat([]byte{13}, 32), zap.NewNop(), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	var refs []string
	var mu sync.Mutex
	keep := func(ref string) { mu.Lock(); defer mu.Unlock(); refs = append(refs, ref) }
	t.Cleanup(func() {
		for _, ref := range refs {
			if e := limiter.Release(ctx, ref); e != nil {
				t.Error(e)
			}
		}
	})

	actor := uuid.NewString()
	for i := 0; i < 5; i++ {
		ref, e := limiter.Reserve(ctx, actor, "post")
		if e != nil {
			t.Fatal(e)
		}
		keep(ref)
		if strings.Contains(ref, actor) {
			t.Fatal("预算键泄漏主体")
		}
	}
	if _, e = limiter.Reserve(ctx, actor, "post"); e == nil {
		t.Fatal("超过十分钟预算仍允许")
	} else {
		var rate *whisper.RateError
		if !errors.As(e, &rate) || rate.Seconds < 1 || rate.Seconds > 600 {
			t.Fatal("重试时间不正确", e)
		}
	}
	if e = limiter.Release(ctx, refs[0]); e != nil {
		t.Fatal(e)
	}
	ref, e := limiter.Reserve(ctx, actor, "post")
	if e != nil {
		t.Fatal("失败命令释放未归还预算", e)
	}
	keep(ref)
	// 发布预算耗尽不阻止撤回独立预算。
	ref, e = limiter.Reserve(ctx, actor, "maintenance")
	if e != nil {
		t.Fatal(e)
	}
	keep(ref)
	// 昵称预算独立于发布，最多三次滚动日提交。
	for i := 0; i < 3; i++ {
		ref, e = limiter.Reserve(ctx, actor, "nickname")
		if e != nil {
			t.Fatal(e)
		}
		keep(ref)
	}
	if _, e = limiter.Reserve(ctx, actor, "nickname"); e == nil {
		t.Fatal("昵称日预算失效")
	}
	// 并发争抢所有窗口只接受五次，不能部分写入另一个窗口。
	concurrentActor := uuid.NewString()
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, e := limiter.Reserve(ctx, concurrentActor, "post")
			if e == nil {
				keep(ref)
			}
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	accepted, limited := 0, 0
	for e := range results {
		if e == nil {
			accepted++
		} else {
			var rate *whisper.RateError
			if !errors.As(e, &rate) {
				t.Fatal(e)
			}
			limited++
		}
	}
	if accepted != 5 || limited != 15 {
		t.Fatal("并发预算失效", accepted, limited)
	}
	// 移动本测试预留的短窗口分值，保留长窗口；验证二十次日预算仍权威。
	rollingActor := uuid.NewString()
	var rolling []lease
	for i := 0; i < 20; i++ {
		ref, e := limiter.Reserve(ctx, rollingActor, "post")
		if e != nil {
			t.Fatal("日预算提前拒绝", i, e)
		}
		keep(ref)
		var value lease
		if json.Unmarshal([]byte(ref), &value) != nil {
			t.Fatal("租约格式无效")
		}
		rolling = append(rolling, value)
		for _, old := range rolling {
			if e = client.ZAdd(ctx, old.Keys[0], redis.Z{Score: float64(time.Now().Add(-11 * time.Minute).UnixMilli()), Member: old.Token}).Err(); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e = limiter.Reserve(ctx, rollingActor, "post"); e == nil {
		t.Fatal("滚动日预算失效")
	}
	for _, tc := range []struct {
		bucket string
		limit  int
	}{{"comment", 20}, {"hug", 60}} {
		t.Run(tc.bucket, func(t *testing.T) {
			actor := uuid.NewString()
			for i := 0; i < tc.limit; i++ {
				ref, e := limiter.Reserve(ctx, actor, tc.bucket)
				if e != nil {
					t.Fatal(e)
				}
				keep(ref)
			}
			if _, e := limiter.Reserve(ctx, actor, tc.bucket); e == nil {
				t.Fatal("超过互动窗口预算仍允许")
			}
		})
	}

	broken := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer broken.Close()
	unavailable, _ := New(broken, bytes.Repeat([]byte{1}, 32), zap.NewNop(), 100*time.Millisecond)
	if _, e = unavailable.Reserve(ctx, uuid.NewString(), "post"); !errors.Is(e, command.ErrUnavailable) {
		t.Fatal("Redis 失败放行", e)
	}
}
