// Package cardworker 装配学生卡的消息唤醒、发件箱转发与数据库补偿调度。
package cardworker

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

// Scanner 共享消息处理与数据库补偿入口，实现必须遵守 context 并复核当前租约。
type Scanner interface {
	transportmq.Handler
	ProcessNext(context.Context, string) (durable.Consumption, error)
}

// Config 显式提供通道和调度预算，不自动创建主题。
type Config = workrunner.Config

// Worker 拥有消息客户端与调度协程，数据库及供应商归调用方所有。
type Worker = workrunner.Worker

// Validate 在创建任何客户端前校验完整预算和学生卡事件类型。
func Validate(cfg Config) error { return workrunner.Validate(cfg, studentcard.Route()) }

type adapter struct{ Scanner }

func (a adapter) ProcessNext(ctx context.Context) (durable.Consumption, error) {
	return a.Scanner.ProcessNext(ctx, "")
}

// Open 完整构造消息转发、订阅和数据库补偿，不迁移或重置凭据。
func Open(ctx context.Context, db *gorm.DB, scanner Scanner, logger *zap.Logger, cfg Config) (*Worker, error) {
	if scanner == nil {
		return nil, durable.ErrInvalid
	}
	return workrunner.Open(ctx, db, adapter{scanner}, logger, cfg, studentcard.Route())
}
