package platform

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// StartHeartbeat 输出无业务数据的进程心跳，返回等待退出的停止函数。
// 调用方只在服务启动成功后启动，并在日志器关闭前停止；心跳不代表业务ready。
func StartHeartbeat(ctx context.Context, log *zap.Logger, service string) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		heartbeatLoop(ctx, log, service, ticker.C)
	}()
	return func() {
		cancel()
		<-done
	}
}

// heartbeatLoop 使用固定事件字段，使无流量时也能检验日志采集新鲜度。
func heartbeatLoop(ctx context.Context, log *zap.Logger, service string, ticks <-chan time.Time) {
	emit := func() {
		log.Info("进程心跳记录完成",
			zap.String("event", "telemetry_heartbeat"),
			zap.String("service", service),
			zap.String("result", "alive"),
		)
	}
	emit()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			emit()
		}
	}
}
