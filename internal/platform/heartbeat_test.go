package platform

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestHeartbeatLifecycle 验证T004/REQ-018、020心跳字段、周期输出与退出后不再使用日志器。
func TestHeartbeatLifecycle(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	emitted := make(chan struct{}, 4)
	log := zap.New(core, zap.Hooks(func(zapcore.Entry) error {
		emitted <- struct{}{}
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		heartbeatLoop(ctx, log, "api", ticks)
	}()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("心跳生命周期操作超时")
		}
	}
	wait(emitted)
	ticks <- time.Now()
	wait(emitted)
	cancel()
	wait(done)
	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("心跳数量=%d", len(entries))
	}
	for _, entry := range entries {
		fields := entry.ContextMap()
		if fields["event"] != "telemetry_heartbeat" || fields["result"] != "alive" || fields["service"] != "api" {
			t.Fatalf("心跳字段不完整: %v", fields)
		}
	}
}

// TestStartHeartbeatStop 验证T004/REQ-020停止等待协程退出，可重复调用而不占用日志器。
func TestStartHeartbeatStop(t *testing.T) {
	emitted := make(chan struct{}, 1)
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core, zap.Hooks(func(zapcore.Entry) error {
		emitted <- struct{}{}
		return nil
	}))
	stop := StartHeartbeat(context.Background(), log, "worker")
	defer stop()
	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("启动后没有初始心跳")
	}
	stop()
	stop()
	if logs.Len() != 1 {
		t.Fatal("停止时心跳数量异常")
	}
}
