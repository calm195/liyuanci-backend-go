package resourceplaza

import (
	"errors"
	"slices"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// 分类特定冲突供HTTP边界明确提示，不能降级为空页。
var (
	// ErrCategoryInUse 表示分类仍被资料引用，不能停用。
	ErrCategoryInUse = errors.New("分类仍有资料引用")
	// ErrCategoryName 表示规范化名称与现有分类冲突。
	ErrCategoryName = errors.New("分类名称已存在")
)

// Config 显式指定事务期限和独立游标密钥，不含默认生产凭据。
type Config struct {
	Command command.Config
	Key     []byte
	Objects FileObjects
}

// Service 借用应用数据库和加密器，每次操作复核会话及岗位；不拥有依赖生命周期。
// 维护元数据、下载密文和私有文件预约，通过显式对象接口访问COS；不访问用户提供的外部下载链接。
type Service struct {
	db    *gorm.DB
	codec *securedata.Codec
	log   *zap.Logger
	cfg   Config
}

// New 验证依赖但不执行DDL或建立业务数据。
func New(db *gorm.DB, log *zap.Logger, codec *securedata.Codec, cfg Config) (*Service, error) {
	if db == nil || log == nil || codec == nil || len(cfg.Key) != 32 || cfg.Command.Timeout <= 0 ||
		cfg.Command.LockTimeout < time.Millisecond {
		return nil, command.ErrInvalid
	}
	cfg.Key = append([]byte(nil), cfg.Key...)
	return &Service{db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), codec, log, cfg}, nil
}

func (s *Service) done(op, rid string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	s.log.Info(
		"资料请求完成",
		zap.String("operation", op),
		zap.String("request_id", rid),
		zap.String("result", result),
		zap.Duration("duration", time.Since(start)),
	)
}

func normalizeError(e error) error {
	if e == nil {
		return nil
	}
	for _, v := range []error{
		command.ErrRetry,
		command.ErrInvalid,
		command.ErrUnauthenticated,
		command.ErrForbidden,
		command.ErrVerificationRequired,
		command.ErrNotFound,
		command.ErrVersionConflict,
		command.ErrIdempotencyConflict,
		command.ErrConflict,
		command.ErrUnavailable,
		ErrCategoryInUse,
		ErrCategoryName,
	} {
		if errors.Is(e, v) {
			return v
		}
	}

	var state interface{ SQLState() string }
	if errors.As(e, &state) && slices.Contains([]string{"40001", "40P01", "55P03"}, state.SQLState()) {
		return command.ErrRetry
	}
	return command.ErrUnavailable
}
