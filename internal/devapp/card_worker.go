package devapp

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/cardworker"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
)

// developmentScanner 仅由隔离开发宿主装配，模拟结论不代表真实内容审核。
type developmentScanner struct{ logger *zap.Logger }

func (s developmentScanner) Scan(ctx context.Context, _ []byte, _ string) (approved bool, err error) {
	defer completed(s.logger, "development_scan", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return false, err
	}
	s.logger.Info("开发图片扫描模拟通过", zap.Bool("simulated", true), zap.String("result", "approved"))
	return true, nil
}

// workerSessions 禁止工作进程误用上传入口；持久工作按数据库账号状态重新授权。
type workerSessions struct{}

func (workerSessions) Current(context.Context, string, string) (studentauth.Me, error) {
	return studentauth.Me{}, command.ErrUnavailable
}

// CheckCardWorker 验证工作身份、业务结构和共享目录，不消费或修改持久任务。
func CheckCardWorker(ctx context.Context, cfg Settings, logger *zap.Logger) (err error) {
	defer completed(logger, "card_worker_check", time.Now(), &err)
	_, closePool, err := pool(ctx, cfg.WorkerDSN, "liyuanci_domain_worker")
	if err != nil {
		return err
	}
	defer func() {
		if closePool() != nil {
			err = command.ErrUnavailable
		}
	}()
	if info, e := os.Stat(cfg.ObjectDirectory); e != nil || !info.IsDir() {
		return command.ErrUnavailable
	}
	return nil
}

// RunCardWorker 仅在隔离开发环境执行；显式配置消息模式时启动转发、订阅和数据库补偿。
// 无消息配置时仅轮询数据库，不迁移、不创建主题或重置历史任务。
// 先等当前处理返回再关闭池，取消中的租约由后续启动恢复。
func RunCardWorker(ctx context.Context, environment string, cfg Settings, logger *zap.Logger) (err error) {
	if logger == nil {
		return command.ErrInvalid
	}
	defer completed(logger, "card_worker", time.Now(), &err)
	if environment != "integration" || cfg.WorkerDSN == "" {
		return command.ErrInvalid
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	db, closePool, err := pool(startup, cfg.WorkerDSN, "liyuanci_domain_worker")
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		started := time.Now()
		failure := closePool()
		completed(logger, "card_worker_pool_close", started, &failure)
		if failure != nil {
			err = command.ErrUnavailable
		}
	}()
	if info, e := os.Stat(cfg.ObjectDirectory); e != nil || !info.IsDir() {
		return command.ErrUnavailable
	}
	codec, err := securedata.New(&localKeys{wrap: cfg.WrapKey, lookup: cfg.LookupKey, logger: logger}, logger)
	if err != nil {
		return err
	}
	service, err := studentcard.New(db, logger, workerSessions{}, codec, &localObjects{cfg.ObjectDirectory, logger}, developmentScanner{logger}, studentcard.Config{
		DecodeSlots: 1,
		MaxBytes:    1 << 20,
		MaxPixels:   4_000_000,
		ReceiptKey:  cfg.CardKey,
		Command:     commandConfig(),
		Lease:       30 * time.Second,
		RetryDelay:  time.Minute,
		MaxAttempts: 3,
	})
	if err != nil {
		return err
	}
	if cfg.CardMQ != nil {
		if err = transportmq.ConfigureSDKLogging(logger); err != nil {
			return err
		}
		worker, failure := cardworker.Open(ctx, db, service, logger, *cfg.CardMQ)
		if failure != nil {
			return failure
		}
		// 先停止消息与补偿，再由外层释放数据库和对象依赖。
		defer func() {
			if worker.Close() != nil {
				err = command.ErrUnavailable
			}
		}()
		logger.Info("开发学生卡消息工作进程启动完成", zap.Bool("simulated", true), zap.String("result", "success"))
		<-ctx.Done()
		return nil
	}
	logger.Info("开发学生卡工作进程启动完成", zap.Bool("simulated", true), zap.String("result", "success"))
	// 每轮结束后等待，空队列和失败都不能形成热循环；工作失败由服务边界持久化和记录。
	for ctx.Err() == nil {
		_, _ = service.ProcessNext(ctx, "")
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}
