package wish

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// TestImageCacheLeaderCancellationDoesNotFailReader 复现发起请求取消时，仍有效的合并读取被连带拒绝。
func TestImageCacheLeaderCancellationDoesNotFailReader(t *testing.T) {
	c := newImageCache()
	leader, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := c.load(leader, "same", func() ([]byte, error) {
			close(entered)
			<-leader.Done()
			return nil, errors.New("上游请求失败")
		})
		first <- err
	}()
	<-entered
	bounded, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	follower := &observedWaitContext{Context: bounded, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		data, err := c.load(follower, "same", func() ([]byte, error) { return []byte("cipher"), nil })
		if err == nil && string(data) != "cipher" {
			err = errors.New("返回数据错误")
		}
		result <- err
	}()
	<-follower.waiting
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal("发起者取消未保留", err)
	}
	if err := <-result; err != nil {
		t.Fatal("有效等待者被连带取消", err)
	}
}

// TestImageCacheCopiesBoundsAndExpiry 验证调用方清零不破坏缓存，容量淘汰、版本隔离、绝对过期及失败重试。
func TestImageCacheCopiesBoundsAndExpiry(t *testing.T) {
	c := newImageCache()
	c.limit = 6
	ctx := context.Background()
	var calls int
	read := func() ([]byte, error) { calls++; return []byte("one"), nil }
	first, _ := c.load(ctx, "a:v1", read)
	clear(first)
	next, _ := c.load(ctx, "a:v1", read)
	if !bytes.Equal(next, []byte("one")) || calls != 1 {
		t.Fatal("返回数据与缓存共享内存", calls)
	}
	_, _ = c.load(ctx, "a:v2", read)
	_, _ = c.load(ctx, "b:v1", read)
	if c.bytes > c.limit || c.entries["a:v1"] != nil {
		t.Fatal("容量未淘汰旧条目")
	}
	element := c.entries["b:v1"]
	entry := element.Value.(imageCacheEntry)
	entry.expires = time.Now().Add(-time.Second)
	element.Value = entry
	_, _ = c.load(ctx, "b:v1", read)
	if calls != 4 {
		t.Fatal("过期未回源", calls)
	}
	failure := errors.New("失败")
	if _, err := c.load(ctx, "failed", func() ([]byte, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := c.load(ctx, "failed", read); err != nil {
		t.Fatal("错误被缓存", err)
	}
}

// TestImageCacheCoalescesAndCancels 验证同图并发只回源一次，取消的等待者不会影响其它请求。
func TestImageCacheCoalescesAndCancels(t *testing.T) {
	c := newImageCache()
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	read := func() ([]byte, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return []byte("cipher"), nil
	}
	var group sync.WaitGroup
	group.Add(1)
	go func() { defer group.Done(); _, _ = c.load(context.Background(), "same", read) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.load(ctx, "same", read); !errors.Is(err, context.Canceled) {
		t.Fatal("取消未返回", err)
	}
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			data, err := c.load(context.Background(), "same", read)
			if err != nil || string(data) != "cipher" {
				t.Error("并发读取失败", err)
			}
			clear(data)
		}()
	}
	close(release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal("重复回源", calls.Load())
	}
}

// TestImageCachePanicDoesNotPoisonKey 回源异常不能留下永久等待的合并条目。
func TestImageCachePanicDoesNotPoisonKey(t *testing.T) {
	c := newImageCache()
	if _, err := c.load(context.Background(), "same", func() ([]byte, error) { panic("synthetic") }); err == nil {
		t.Fatal("异常回源未失败关闭")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	data, err := c.load(ctx, "same", func() ([]byte, error) { return []byte("retry"), nil })
	if err != nil || string(data) != "retry" {
		t.Fatal("异常后无法重试", err)
	}
}
