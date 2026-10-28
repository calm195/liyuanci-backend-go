package whisper

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// Service 不拥有客户端生命周期；不注册路由、不迁移、不默认产生审核工作。
type Service struct {
	db        *gorm.DB
	logger    *zap.Logger
	codec     *securedata.Codec
	engine    *command.Engine
	catalog   Catalog
	admission Admission
	review    ReviewSink
	config    Config
}

// NewService 的 review 可为空；此时草稿与读取可验证，送审失败关闭。
func NewService(db *gorm.DB, logger *zap.Logger, codec *securedata.Codec, catalog Catalog, admission Admission, review ReviewSink, cfg Config) (*Service, error) {
	if codec == nil || catalog == nil || admission == nil || len(cfg.ReceiptKey) < 32 || len(cfg.CursorKey) < 32 {
		return nil, command.ErrInvalid
	}
	engine, e := command.New(db, logger, cfg.Command)
	if e != nil {
		return nil, e
	}
	cfg.ReceiptKey = append([]byte(nil), cfg.ReceiptKey...)
	cfg.CursorKey = append([]byte(nil), cfg.CursorKey...)
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), logger: logger, codec: codec, engine: engine, catalog: catalog, admission: admission, review: review, config: cfg}, nil
}

func (s *Service) completed(operation, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	*err = boundaryError(*err)
	result := "success"
	if *err != nil {
		result = "failure"
	}
	s.logger.Info("树洞操作完成", zap.String("operation", operation), zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

func boundaryError(err error) error {
	if err == nil {
		return nil
	}
	var rate *RateError
	if errors.As(err, &rate) {
		return rate
	}
	for _, known := range []error{ErrModuleUnavailable, ErrCapsuleLimit, ErrVerificationRequired, ErrModeUnavailable, ErrDictionaryChanged, ErrNicknameNotReady, command.ErrClientUpgradeRequired, command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrConflict, command.ErrBlockedInteraction, command.ErrInternal, command.ErrUnavailable, command.ErrRetry} {
		if errors.Is(err, known) {
			return known
		}
	}
	return command.ErrUnavailable
}

func (s *Service) transaction(ctx context.Context, fn func(*gorm.DB, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		return fn(db, now)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

func (s *Service) resources(ctx context.Context) (CatalogSnapshot, error) {
	c, e := s.catalog.Load(ctx)
	if e != nil {
		return CatalogSnapshot{}, command.ErrUnavailable
	}
	return normalizeCatalog(c)
}

// Configuration 返回当前资料可用性，缺资料不伪造标签或援助号码。
func (s *Service) Configuration(ctx context.Context, rid string) (out ModuleConfig, err error) {
	defer s.completed("configuration", rid, time.Now(), &err)
	c, e := s.resources(ctx)
	if e != nil {
		return out, e
	}
	err = s.transaction(ctx, func(_ *gorm.DB, now time.Time) error {
		out = ModuleConfig{ContractVersion: "whispers/1.0.0", Enabled: s.config.Enabled, ServerNow: timestamp(now), SemiAvailable: c.Grades.Version != nil, GradeDictionary: c.Grades, Limits: Limits{500, 2, 20, 60, 31536000, 20}, ReportTypes: c.Reports, BlockAPI: "opaque-v1"}
		return nil
	})
	return
}

// Help 返回经显式核验的公众资料；此操作不要求会话或模块开关。
func (s *Service) Help(ctx context.Context, rid string) (out HelpDirectory, err error) {
	defer s.completed("help", rid, time.Now(), &err)
	c, e := s.resources(ctx)
	return c.Help, e
}

// Workspace 返回当前会话和限制；受罚账号仍可进入本人空间。
func (s *Service) Workspace(ctx context.Context, token, rid string) (out Workspace, err error) {
	defer s.completed("workspace", rid, time.Now(), &err)
	if !s.config.Enabled {
		return out, ErrModuleUnavailable
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		a, e := reader(ctx, db, token, now, false)
		if e != nil {
			return e
		}
		out = Workspace{ServerNow: timestamp(now), Verification: a.Verification, WriteRestricted: a.Restricted, AllowedActions: []string{}}
		if !a.Restricted {
			out.AllowedActions = append(out.AllowedActions, "whisperCapsuleCreate")
		}
		if !a.Restricted && a.Verification == "verified" {
			out.AllowedActions = append(out.AllowedActions, "whisperPostCreate")
		}
		return nil
	})
	return
}

func (s *Service) encrypt(ctx context.Context, purpose, id string, version int64, value any) (json.RawMessage, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return nil, command.ErrInvalid
	}
	defer clear(b)
	envelope, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, b)
	if e != nil {
		return nil, command.ErrUnavailable
	}
	b, e = json.Marshal(envelope)
	return b, e
}

func (s *Service) decrypt(ctx context.Context, purpose, id string, version int64, cipher json.RawMessage, out any) error {
	var envelope securedata.Envelope
	if json.Unmarshal(cipher, &envelope) != nil {
		return command.ErrUnavailable
	}
	b, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, envelope)
	if e != nil {
		return command.ErrUnavailable
	}
	defer clear(b)
	if json.Unmarshal(b, out) != nil {
		return command.ErrUnavailable
	}
	return nil
}
