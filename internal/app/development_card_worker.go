package app

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
)

// developmentWorkerSessions 禁止工作进程误用上传入口；持久工作按数据库账号状态重新授权。
type developmentWorkerSessions struct{}

// Current 禁止本地worker作为学生会话来源，开发模拟不绕过授权。
func (developmentWorkerSessions) Current(context.Context, string, string) (studentauth.Me, error) {
	return studentauth.Me{}, command.ErrUnavailable
}

// CheckCardWorker 验证工作身份、业务结构和共享目录，不消费或修改持久任务。
func CheckCardWorker(ctx context.Context, cfg DevelopmentSettings, logger *zap.Logger) (err error) {
	defer finishDevelopment(logger, "card_worker_check", time.Now(), &err)
	_, closePool, err := cfg.pool(ctx, logger)
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

// RunCardWorker 仅在隔离开发环境扫描数据库持久任务，保留租约、重试与幂等语义。
// 仅轮询数据库，不迁移或重置历史任务。
// 先等当前处理返回再关闭池，取消中的租约由后续启动恢复。
func RunCardWorker(ctx context.Context, environment string, cfg DevelopmentSettings, logger *zap.Logger) (err error) {
	if logger == nil {
		return command.ErrInvalid
	}
	defer finishDevelopment(logger, "card_worker", time.Now(), &err)
	if (environment != "integration" && environment != "development") || cfg.DatabaseDSN == "" {
		return command.ErrInvalid
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	db, closePool, err := cfg.pool(startup, logger)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		started := time.Now()
		failure := closePool()
		finishDevelopment(logger, "card_worker_pool_close", started, &failure)
		if failure != nil {
			err = command.ErrUnavailable
		}
	}()
	if info, e := os.Stat(cfg.ObjectDirectory); e != nil || !info.IsDir() {
		return command.ErrUnavailable
	}
	codec, err := securedata.New(&local.IdentityKeys{WrapKey: cfg.WrapKey, LookupKey: cfg.LookupKey, Logger: logger}, logger)
	if err != nil {
		return err
	}
	service, err := studentcard.New(db, logger, developmentWorkerSessions{}, codec, &local.Objects{Root: cfg.ObjectDirectory, Logger: logger}, local.Scanner{Logger: logger}, studentcard.Config{
		DecodeSlots: 1,
		MaxBytes:    1 << 20,
		MaxPixels:   4_000_000,
		ReceiptKey:  cfg.CardKey,
		Command:     developmentCommandConfig(),
		Lease:       30 * time.Second,
		RetryDelay:  time.Minute,
		MaxAttempts: 3,
	})
	if err != nil {
		return err
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
