package cardworker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
)

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

// TestCloseWaitsForOperations 覆盖 T007/T011、REQ-015/018/020 的资源归属与关闭顺序。
func TestCloseWaitsForOperations(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var subscriberClosed, senderClosed atomic.Int32
	run := func(ctx context.Context) error {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		<-release
		return ctx.Err()
	}
	w := startWorker(func(ctx context.Context) (bool, error) { return false, run(ctx) }, func(ctx context.Context, _ string) (durable.Consumption, error) {
		return durable.Consumption{}, run(ctx)
	}, closeFunc(func() error { subscriberClosed.Add(1); return errors.New("SECRET") }), closeFunc(func() error {
		if active.Load() != 0 || subscriberClosed.Load() != 1 {
			t.Error("在途操作结束前关闭生产者")
		}
		senderClosed.Add(1)
		return nil
	}), zap.NewNop(), Config{RelayInterval: time.Hour, ScanInterval: time.Hour, OperationTimeout: time.Minute})
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("调度没有启动")
		}
	}
	closed := make(chan error, 2)
	go func() { closed <- w.Close() }()
	select {
	case <-closed:
		t.Fatal("未等待在途操作")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	go func() { closed <- w.Close() }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-closed:
			if err != durable.ErrUnavailable {
				t.Errorf("关闭错误未脱敏：%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("关闭超时")
		}
	}
	if subscriberClosed.Load() != 1 || senderClosed.Load() != 1 {
		t.Fatal("重复关闭自有资源")
	}
}

// TestPollRecovery 验证失败与 panic 不终止数据库补偿，也不在日志中泄漏原始错误。
func TestPollRecovery(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	poll(ctx, time.Millisecond, time.Second, zap.New(core), "scan", func(context.Context) error {
		calls++
		switch calls {
		case 1:
			panic("SECRET")
		case 2:
			return errors.New("SECRET")
		default:
			cancel()
			return nil
		}
	})
	if calls != 3 || logs.Len() != 3 {
		t.Fatal("异常后未继续调度")
	}
	for _, entry := range logs.All() {
		if entry.ContextMap()["result"] != "failure" {
			t.Fatal("取消或失败误报成功")
		}
		if len(entry.ContextMap()) != 3 {
			t.Fatal("日志含额外供应商字段")
		}
	}
}

// TestPollDeadline 验证单轮超时后等待重试，不将迟到成功标成完成。
func TestPollDeadline(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	poll(ctx, time.Millisecond, time.Millisecond, zap.New(core), "relay", func(bounded context.Context) error {
		calls++
		<-bounded.Done()
		if calls == 2 {
			cancel()
		}
		return nil
	})
	if calls != 2 {
		t.Fatal("超时没有重试")
	}
	for _, entry := range logs.All() {
		if entry.ContextMap()["result"] != "failure" {
			t.Fatal("超时误报成功")
		}
	}
}

// TestCloseIdleWorker 验证长间隔调度可被立即取消，并发 Close 不竞态。
func TestCloseIdleWorker(t *testing.T) {
	var closed atomic.Int32
	w := startWorker(func(context.Context) (bool, error) { return false, nil }, func(context.Context, string) (durable.Consumption, error) {
		return durable.Consumption{Deferred: true}, nil
	}, closeFunc(func() error { closed.Add(1); return nil }), closeFunc(func() error { closed.Add(1); return nil }), zap.NewNop(), Config{RelayInterval: time.Hour, ScanInterval: time.Hour, OperationTimeout: time.Second})
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if w.Close() != nil {
				t.Error("关闭失败")
			}
		}()
	}
	group.Wait()
	if closed.Load() != 2 {
		t.Fatal("资源关闭次数错误")
	}
}

func TestOpenRejectsMissingDependencies(t *testing.T) {
	if _, err := Open(context.Background(), nil, nil, zap.NewNop(), Config{}); err != durable.ErrInvalid {
		t.Fatal("缺依赖仍启动")
	}
	if _, err := Open(context.Background(), nil, nil, nil, Config{}); err != durable.ErrInvalid {
		t.Fatal("缺日志仍启动")
	}
}

// TestPollSuccessAndIdleCancel 验证成功完成日志与空闲取消，不启动取消后的额外轮询。
func TestPollSuccessAndIdleCancel(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		poll(ctx, time.Hour, time.Second, zap.New(core), "scan", func(context.Context) error { return nil })
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for logs.Len() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("缺成功完成日志")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("空闲取消超时")
	}
	if logs.Len() != 1 || logs.All()[0].ContextMap()["result"] != "success" {
		t.Fatal("成功或停止语义错误")
	}
}
