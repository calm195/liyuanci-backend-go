package workrunner

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
)

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

type databaseScanner struct {
	started  chan struct{}
	returned chan struct{}
}

func (s databaseScanner) Consume(context.Context, durable.Event) (durable.Consumption, error) {
	return durable.Consumption{}, nil
}

func (s databaseScanner) ProcessNext(ctx context.Context) (durable.Consumption, error) {
	close(s.started)
	<-ctx.Done()
	close(s.returned)
	return durable.Consumption{}, ctx.Err()
}

// TestDatabaseRunnerClose 验证无MQ的补偿在关闭时取消并等待在途处理，不要求消息客户端。
func TestDatabaseRunnerClose(t *testing.T) {
	s := databaseScanner{make(chan struct{}), make(chan struct{})}
	w, err := OpenDatabase(context.Background(), s, zap.NewNop(), time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.started:
	case <-time.After(time.Second):
		t.Fatal("未启动补偿")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.returned:
	default:
		t.Fatal("未等待补偿退出")
	}
	if _, err := OpenDatabase(context.Background(), nil, zap.NewNop(), time.Second, time.Second); err == nil {
		t.Fatal("接受空处理器")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenDatabase(ctx, s, zap.NewNop(), time.Second, time.Second); err == nil {
		t.Fatal("取消后启动")
	}
}

type maintainedScanner struct {
	databaseScanner
	maintenanceStarted  chan struct{}
	maintenanceReturned chan struct{}
}

func (s maintainedScanner) Maintain(ctx context.Context) error {
	close(s.maintenanceStarted)
	<-ctx.Done()
	close(s.maintenanceReturned)
	return ctx.Err()
}

// TestMaintenanceLifecycle 验证危机通知可独立阻塞而不阻止扫描，关闭同时取消并等待两种在途操作。
func TestMaintenanceLifecycle(t *testing.T) {
	s := maintainedScanner{
		databaseScanner{make(chan struct{}), make(chan struct{})},
		make(chan struct{}),
		make(chan struct{}),
	}
	w, e := OpenDatabase(context.Background(), s, zap.NewNop(), time.Hour, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	for _, ch := range []chan struct{}{s.started, s.maintenanceStarted} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("扫描或维护未独立启动")
		}
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	for _, ch := range []chan struct{}{s.returned, s.maintenanceReturned} {
		select {
		case <-ch:
		default:
			t.Fatal("关闭未等待维护退出")
		}
	}
}
