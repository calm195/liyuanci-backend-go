// Package workrunner 管理领域持久任务的数据库扫描、补偿及关闭生命周期。
package workrunner

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
)

// Scanner 共享消息处理与数据库补偿入口；实现必须遵守 context 并复核当前租约。
type Scanner interface {
	ProcessNext(context.Context) (durable.Consumption, error)
}

// Maintainer 允许领域提供独立的有界待办补偿，和扫描共用宿主取消及等待生命周期。
type Maintainer interface{ Maintain(context.Context) error }

func maintenance(scanner Scanner) func(context.Context) error {
	if m, ok := scanner.(Maintainer); ok {
		return m.Maintain
	}
	return nil
}

// Config 保存数据库任务轮询间隔和单轮预算。
type Config struct{ ScanInterval, OperationTimeout time.Duration }

// Worker 拥有调度协程，数据库及供应商归宿主所有。
type Worker struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	logger *zap.Logger
}

// OpenDatabase 仅启动有界数据库补偿，不构造消息客户端或要求 Broker。
// 调用方须先 Close 并等待在途处理结束，再释放数据库和供应商。
func OpenDatabase(ctx context.Context, scanner Scanner, logger *zap.Logger, interval, timeout time.Duration) (*Worker, error) {
	if scanner == nil || logger == nil || ctx.Err() != nil || interval < time.Millisecond || timeout <= 0 {
		return nil, durable.ErrInvalid
	}
	return startWorker(scanner.ProcessNext, logger, Config{ScanInterval: interval, OperationTimeout: timeout}, maintenance(scanner)), nil
}

func startWorker(scan func(context.Context) (durable.Consumption, error), logger *zap.Logger, cfg Config, maintain ...func(context.Context) error) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{cancel: cancel, done: make(chan struct{}), logger: logger}
	var running sync.WaitGroup
	running.Add(1)
	go func() {
		defer running.Done()
		poll(ctx, cfg.ScanInterval, cfg.OperationTimeout, logger, "scan", func(ctx context.Context) error { _, err := scan(ctx); return err })
	}()
	for _, run := range maintain {
		if run != nil {
			running.Add(1)
			go func() {
				defer running.Done()
				poll(ctx, cfg.ScanInterval, cfg.OperationTimeout, logger, "maintenance", run)
			}()
		}
	}
	go func() { running.Wait(); close(w.done) }()
	return w
}

// poll 每轮结束后才等待下一轮，失败和空队列均等待，避免异常时热循环与同职责重叠。
func poll(ctx context.Context, interval, timeout time.Duration, logger *zap.Logger, operation string, run func(context.Context) error) {
	for ctx.Err() == nil {
		start := time.Now()
		err := func() (err error) {
			bounded, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			defer func() {
				if recover() != nil {
					err = durable.ErrConfiguration
				}
			}()
			err = run(bounded)
			if err == nil {
				err = bounded.Err()
			}
			return err
		}()
		complete(logger, operation, start, err)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Close 幂等取消并等待所有在途任务，返回后宿主才可关闭数据库。
func (w *Worker) Close() (err error) {
	start := time.Now()
	defer func() { complete(w.logger, "close", start, err) }()
	w.once.Do(func() { w.cancel(); <-w.done })
	return nil
}

func complete(logger *zap.Logger, operation string, start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	// 不输出适配器原始错误，避免供应商地址、对象键及凭据越过日志边界。
	logger.Info("领域工作调度完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
