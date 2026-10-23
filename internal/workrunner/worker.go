// Package workrunner 管理领域工作进程的消息唤醒、转发和数据库补偿生命周期。
package workrunner

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
)

// Scanner 共享消息处理与数据库补偿入口；实现必须遵守 context 并复核当前租约。
type Scanner interface {
	transportmq.Handler
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

// Config 显式提供通道和调度预算；没有生产默认参数或自动创建主题的行为。
type Config struct {
	MQ               transportmq.Config
	Relay            durable.Config
	RelayInterval    time.Duration
	ScanInterval     time.Duration
	OperationTimeout time.Duration
}

// Worker 仅拥有消息客户端与调度协程，数据库及扫描供应商仍由调用方拥有。
type Worker struct {
	cancel     context.CancelFunc
	done       chan struct{}
	once       sync.Once
	closeErr   error
	subscriber closer
	sender     closer
	logger     *zap.Logger
}

type closer interface{ Close() error }

// Validate 在创建任何消息客户端前校验完整预算和领域事件类型，避免部分启动后才发现调度配置无效。
func Validate(cfg Config, route durable.Route) error {
	if transportmq.Validate(cfg.MQ) != nil || cfg.MQ.EventType != route.EventType ||
		cfg.RelayInterval < time.Millisecond || cfg.ScanInterval < time.Millisecond || cfg.OperationTimeout <= 0 ||
		cfg.Relay.DatabaseTimeout <= 0 || cfg.Relay.SendTimeout <= 0 || cfg.Relay.LeaseDuration <= cfg.Relay.SendTimeout ||
		cfg.Relay.LeaseDuration < time.Millisecond || cfg.Relay.RetryDelay < time.Millisecond || cfg.Relay.MaxAttempts < 1 || cfg.Relay.MaxAttempts > 1000 {
		return durable.ErrInvalid
	}
	return nil
}

// Open 在调用方完成依赖构造及 SDK 日志配置后启动，不执行迁移、初始化凭据或启用探针。
func Open(ctx context.Context, db *gorm.DB, scanner Scanner, logger *zap.Logger, cfg Config, route durable.Route) (out *Worker, err error) {
	if logger == nil {
		return nil, durable.ErrInvalid
	}
	start := time.Now()
	defer func() { complete(logger, "open", start, err) }()
	if db == nil || db.Statement == nil || scanner == nil || Validate(cfg, route) != nil || ctx.Err() != nil {
		return nil, durable.ErrInvalid
	}
	sender, err := transportmq.OpenSender(ctx, cfg.MQ, logger)
	if err != nil {
		return nil, err
	}
	// 启动失败按所有权逆序释放，不能留下没有调度者的生产客户端。
	defer func() {
		if err != nil {
			_ = sender.Close()
		}
	}()
	relay, err := durable.NewRelay(db, logger, sender, route, cfg.Relay)
	if err != nil {
		return nil, err
	}
	subscriber, err := transportmq.OpenSubscriber(ctx, cfg.MQ, scanner, logger)
	if err != nil {
		return nil, err
	}
	// 启动 context 不充当运行期限；显式 Close 负责取消并等待全部在途操作。
	return startWorker(relay.Once, scanner.ProcessNext, subscriber, sender, logger, cfg, maintenance(scanner)), nil
}

// OpenDatabase 仅启动有界数据库补偿，不构造消息客户端或要求 Broker。
// 调用方须先 Close 并等待在途处理结束，再释放数据库和供应商。
func OpenDatabase(ctx context.Context, scanner Scanner, logger *zap.Logger, interval, timeout time.Duration) (*Worker, error) {
	if scanner == nil || logger == nil || ctx.Err() != nil || interval < time.Millisecond || timeout <= 0 {
		return nil, durable.ErrInvalid
	}
	return startWorker(nil, scanner.ProcessNext, nil, nil, logger, Config{ScanInterval: interval, OperationTimeout: timeout}, maintenance(scanner)), nil
}

func startWorker(relay func(context.Context) (bool, error), scan func(context.Context) (durable.Consumption, error), subscriber, sender closer, logger *zap.Logger, cfg Config, maintain ...func(context.Context) error) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{cancel: cancel, done: make(chan struct{}), subscriber: subscriber, sender: sender, logger: logger}
	var running sync.WaitGroup
	running.Add(1)
	if relay != nil {
		running.Add(1)
		go func() {
			defer running.Done()
			poll(ctx, cfg.RelayInterval, cfg.OperationTimeout, logger, "relay", func(ctx context.Context) error { _, err := relay(ctx); return err })
		}()
	}
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

// Close 幂等停止调度并取消在途操作，先等待消费和轮询退出，再关闭生产者。
// 调用方必须在返回后才释放数据库和供应商，关闭失败也继续释放其余自有资源。
func (w *Worker) Close() (err error) {
	start := time.Now()
	defer func() { complete(w.logger, "close", start, err) }()
	w.once.Do(func() {
		w.cancel()
		if w.subscriber != nil && w.subscriber.Close() != nil {
			w.closeErr = durable.ErrUnavailable
		}
		<-w.done
		if w.sender != nil && w.sender.Close() != nil {
			w.closeErr = durable.ErrUnavailable
		}
	})
	return w.closeErr
}

func complete(logger *zap.Logger, operation string, start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	// 不输出适配器原始错误，避免供应商地址、对象键及凭据越过日志边界。
	logger.Info("领域工作调度完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
