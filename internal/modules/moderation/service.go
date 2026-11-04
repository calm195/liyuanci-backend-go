// Package moderation 管理不可变词库、当前版本扫描和案件级人工复核。
package moderation

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// Config 显式提供技术预算和独立游标密钥，不隐含生产规则。
type Config struct {
	Command    command.Config
	CursorKey  []byte
	MaxEntries int
}

// Service 借用应用连接池，每次操作复核后台会话和岗位，不拥有数据库或密钥服务生命周期。
type Service struct {
	db     *gorm.DB
	engine *command.Engine
	logger *zap.Logger
	codec  *securedata.Codec
	cfg    Config
}

// New 只构造模块，不初始化词库、不迁移或注册路由。
func New(db *gorm.DB, log *zap.Logger, codec *securedata.Codec, cfg Config) (*Service, error) {
	if log == nil || codec == nil || len(cfg.CursorKey) < 32 || cfg.MaxEntries < 1 || cfg.MaxEntries > 10000 {
		return nil, command.ErrInvalid
	}
	engine, err := command.New(db, log, cfg.Command)
	if err != nil {
		return nil, err
	}
	cfg.CursorKey = append([]byte(nil), cfg.CursorKey...)
	return &Service{
		db:     db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}),
		engine: engine,
		logger: log,
		codec:  codec,
		cfg:    cfg,
	}, nil
}

func validID(id string) bool {

	u, e := uuid.Parse(id)
	return e == nil && u.String() == id
}

func kind(k string) bool {
	return slices.Contains([]string{"crisis", "school", "ordinary_violation"}, k)
}

func safe(err error) error {
	for _, e := range []error{
		command.ErrInvalid,
		command.ErrUnauthenticated,
		command.ErrForbidden,
		command.ErrNotFound,
		command.ErrVersionConflict,
		command.ErrIdempotencyConflict,
		command.ErrConflict,
		command.ErrRetry,
		command.ErrInternal,
	} {
		if errors.Is(err, e) {
			return e
		}
	}
	if err != nil {
		return command.ErrUnavailable
	}
	return nil
}

// done 必须直接defer调用，才能恢复同一请求的panic并统一记录脱敏后的失败结果。
func (s *Service) done(op, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	*err = safe(*err)
	result := "success"
	if *err != nil {
		result = "failure"
	}
	fields := []zap.Field{
		zap.String("operation", op),
		zap.String("result", result),
		zap.Duration("duration", time.Since(start)),
	}
	if len(rid) <= 128 && !strings.ContainsAny(rid, "\r\n") {
		fields = append(fields, zap.String("request_id", rid))
	}
	s.logger.Info("内容审核操作完成", fields...)
}

func text(s string, max int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
