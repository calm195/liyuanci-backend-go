package wish

import (
	"bytes"
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type imageCacheEntry struct {
	key     string
	data    []byte
	expires time.Time
}

type imageFlight struct {
	done     chan struct{}
	data     []byte
	err      error
	canceled bool
}

// imageCache 只保留密文，按字节数、条目数和绝对期限限制内存；不缓存可见性和会话授权。
type imageCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	pending map[string]*imageFlight
	bytes   int
	limit   int
	ttl     time.Duration
}

func newImageCache() *imageCache {
	return &imageCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		pending: make(map[string]*imageFlight),
		limit:   32 << 20,
		ttl:     2 * time.Minute,
	}
}

func (c *imageCache) remove(element *list.Element) {
	entry := element.Value.(imageCacheEntry)
	c.bytes -= len(entry.data)
	clear(entry.data)
	delete(c.entries, entry.key)
	c.order.Remove(element)
}

func (c *imageCache) load(ctx context.Context, key string, read func() ([]byte, error)) ([]byte, error) {
	for {
		data, err, retry := c.loadOnce(ctx, key, read)
		if !retry {
			return data, err
		}
		// 只有回源发起者取消才重新竞争读取；有效调用方不继承另一请求的取消状态。
	}
}

func (c *imageCache) loadOnce(ctx context.Context, key string, read func() ([]byte, error)) ([]byte, error, bool) {
	if err := ctx.Err(); err != nil {
		return nil, err, false
	}
	c.mu.Lock()
	now := time.Now()
	for _, element := range c.entries {
		if !now.Before(element.Value.(imageCacheEntry).expires) {
			c.remove(element)
		}
	}
	if element := c.entries[key]; element != nil {
		c.order.MoveToFront(element)
		data := bytes.Clone(element.Value.(imageCacheEntry).data)
		c.mu.Unlock()
		return data, nil, false
	}
	if flight := c.pending[key]; flight != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err(), false
		case <-flight.done:
			if flight.canceled && ctx.Err() == nil {
				return nil, nil, true
			}
			return bytes.Clone(flight.data), flight.err, false
		}
	}
	flight := &imageFlight{done: make(chan struct{})}
	c.pending[key] = flight
	c.mu.Unlock()

	data, err := readImageCacheEntry(read)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		err = ctx.Err()
		flight.canceled = true
	}
	if err == nil && len(data) > 0 && len(data) <= c.limit {
		for c.bytes+len(data) > c.limit || len(c.entries) >= 128 {
			c.remove(c.order.Back())
		}
		c.entries[key] = c.order.PushFront(imageCacheEntry{key: key, data: bytes.Clone(data), expires: time.Now().Add(c.ttl)})
		c.bytes += len(data)
	}
	// 在途请求只合并同一不可变对象的读取；每位调用方随后独立检查权限。
	if err == nil {
		flight.data = data
	} else {
		clear(data)
	}
	flight.err = err
	delete(c.pending, key)
	close(flight.done)
	return bytes.Clone(flight.data), err, false
}

// readImageCacheEntry 将异常回源归为失败，确保在途条目总能释放，后续请求仍可重试。
func readImageCacheEntry(read func() ([]byte, error)) (data []byte, err error) {
	defer func() {
		if recover() != nil {
			clear(data)
			data = nil
			err = command.ErrUnavailable
		}
	}()
	return read()
}
