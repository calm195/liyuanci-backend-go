// Package studentcard 提供有界学生卡加密上传和外部扫描工作，不把图片通过视为学生认证。
package studentcard

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
)

// Sessions 只作为事务外有界图像计算的身份准入，写入仍由命令引擎锁后重新授权。
type Sessions interface {
	Current(context.Context, string, string) (studentauth.Me, error)
}

// Objects 要求同一随机key及相同密文幂等写入；不同密文不得覆盖，返回不可变对象版本。
// 实现须保持私有、遵守context且不记录对象内容或返回可公开读取URL。
type Objects interface {
	PutOnce(context.Context, string, []byte) (string, error)
}

// Scanner 仅处理受控内存中的规范化图片；拒绝/故障分离，供应商不得保留图片或记录请求正文。
type Scanner interface {
	Scan(context.Context, []byte, string) (bool, error)
}

// Config 显式限制上传资源和工作租约/重试预算，不包含默认生产参数。
type Config struct {
	DecodeSlots int
	MaxBytes    int64
	MaxPixels   int64
	ReceiptKey  []byte
	Command     command.Config
	Lease       time.Duration
	RetryDelay  time.Duration
	MaxAttempts int
}

// Asset 是本人可读的最小扫描投影，不返回对象key、密文或下载URL。
type Asset struct {
	AssetID    string `json:"asset_id"`
	ScanStatus string `json:"scan_status"`
}

// Service 不拥有注入资源；API与worker可使用权限不同的数据库池分别构造服务。
type Service struct {
	db       *gorm.DB
	engine   *command.Engine
	logger   *zap.Logger
	sessions Sessions
	codec    *securedata.Codec
	objects  Objects
	scanner  Scanner
	config   Config
	slots    chan struct{}
}

// New 构造显式依赖，不运行迁移或启动后台任务。
func New(db *gorm.DB, logger *zap.Logger, sessions Sessions, codec *securedata.Codec, objects Objects, scanner Scanner, cfg Config) (*Service, error) {
	if logger == nil || sessions == nil || codec == nil || objects == nil || scanner == nil || cfg.DecodeSlots < 1 || cfg.DecodeSlots > 16 || cfg.MaxBytes < 1 || cfg.MaxBytes > int64(int(^uint(0)>>1))/4 || cfg.MaxPixels < 1 || len(cfg.ReceiptKey) < 32 || cfg.Lease < cfg.Command.Timeout || cfg.RetryDelay < time.Millisecond || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 1000 {
		return nil, command.ErrInvalid
	}
	engine, e := command.New(db, logger, cfg.Command)
	if e != nil {
		return nil, e
	}
	cfg.ReceiptKey = slices.Clone(cfg.ReceiptKey)
	return &Service{
		db:       db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}),
		engine:   engine,
		logger:   logger,
		sessions: sessions,
		codec:    codec,
		objects:  objects,
		scanner:  scanner,
		config:   cfg,
		slots:    make(chan struct{}, cfg.DecodeSlots),
	}, nil
}

// Route 返回固定扫描路由，供显式发件箱/订阅装配共享，不接受客户端路由。
func Route() durable.Route {
	return durable.Route{Kind: "student_card.scan", TargetKind: "asset", EventType: "student_card.scan_requested"}
}
func validID(id string) bool { parsed, e := uuid.Parse(id); return e == nil && parsed.String() == id }
func validKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}

// ErrBusy 表示本进程图片计算资源已满，可在稍后重试，不能伪报上传成功。
var ErrBusy = studentauth.ErrRateLimited

func safe(e error) error {
	if e == nil {
		return nil
	}
	for _, known := range []error{command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrConflict, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrInternal, ErrBusy} {
		if errors.Is(e, known) {
			return known
		}
	}
	return command.ErrUnavailable
}
func (s *Service) complete(operation, requestID string, start time.Time, err error, fields ...zap.Field) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	if len(requestID) > 128 || strings.ContainsFunc(requestID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) {
		requestID = ""
	}
	s.logger.Info("学生卡操作完成", append([]zap.Field{zap.String("operation", operation), zap.String("request_id", requestID), zap.String("result", result), zap.Duration("duration", time.Since(start))}, fields...)...)
}

// Upload 只在根事务中持久化信封密文和工作；网络外发由worker在提交后执行。
func (s *Service) Upload(ctx context.Context, token, key, requestID, mime string, source io.Reader) (out Asset, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		if err != nil {
			out = Asset{}
		}
		s.complete("upload", requestID, start, err)
	}()
	if !validKey(key) {
		return out, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	if _, e := s.sessions.Current(ctx, token, requestID); e != nil {
		return out, e
	}
	// 解码/重编码和加密使用有界并发，避免多个合法账号同时耗尽图片内存。
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return out, ErrBusy
	}
	image, e := normalizeImage(source, mime, s.config.MaxBytes, s.config.MaxPixels)
	if e != nil {
		return out, e
	}
	defer clear(image)
	// 回执只绑定用途隔离摘要，不保存原图、文件名或可猜测的裸摘要。
	mac := hmac.New(sha256.New, s.config.ReceiptKey)
	_, _ = mac.Write([]byte("student_card_upload\x00" + mime + "\x00"))
	_, _ = mac.Write(image)
	digest := hex.EncodeToString(mac.Sum(nil))
	id := uuid.NewString()
	payload, e := s.codec.EncryptObject(ctx, securedata.Binding{Purpose: "student_card", ObjectID: id, Version: 1}, bytes.NewReader(image), s.config.MaxBytes)
	if e != nil {
		return out, e
	}
	encoded, e := json.Marshal(payload)
	if e != nil {
		return out, e
	}
	objectKey := uuid.NewString()
	keyEnvelope, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "student_card_key", ObjectID: id, Version: 1}, []byte(objectKey))
	if e != nil {
		return out, e
	}
	encodedKey, e := json.Marshal(keyEnvelope)
	if e != nil {
		return out, e
	}
	definition := command.Definition{
		Kind: "student_card.upload", Method: "POST", ResourcePath: "/api/v1/me/student-card-assets", ActorKind: "student", Access: command.Ordinary,
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(context.Context, *command.Tx) error { return nil },
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			asset := entity.Asset{ID: id, OwnerID: tx.Actor().ID, Purpose: "student_card", EncryptedObjectKey: encodedKey, ObjectVersion: "pending", WrappedKeyRef: payload.KeyRef, Status: "pending", ScanStatus: "pending", Version: 1}
			if e := createAsset(ctx, tx.DB(), asset, encoded, "image/png"); e != nil {
				return "", e
			}
			draft := durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: id, AggregateVersion: 1}
			if e := durable.Append(ctx, tx.DB(), s.logger, Route(), draft); e != nil {
				return "", e
			}
			return id, nil
		},
		Resolve: func(ctx context.Context, tx *command.Tx, id string) error {
			asset, e := readAsset(ctx, tx.DB(), id)
			if e != nil {
				return e
			}
			if asset.OwnerID != tx.Actor().ID {
				return command.ErrNotFound
			}
			out = Asset{AssetID: asset.ID, ScanStatus: asset.ScanStatus}
			return nil
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: requestID, Request: struct {
		Digest string `json:"digest"`
	}{digest}})
	return out, err
}

// Get 只允许当前本人读取扫描状态；拒绝返回私有资源引用，未知与非本人统一404。
func (s *Service) Get(ctx context.Context, token, id, requestID string) (out Asset, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		if err != nil {
			out = Asset{}
		}
		s.complete("get", requestID, start, err)
	}()
	if !validID(id) {
		return out, command.ErrInvalid
	}
	// 读取通过同一事务持guard复核会话，避免准入快照与注销之间存在读取窗口。
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		account, e := authorizeStudent(ctx, db, token, s.config.Command.LockTimeout)
		if e != nil {
			return e
		}
		asset, e := readAsset(ctx, db, id)
		if e != nil {
			return e
		}
		if asset.OwnerID != account || asset.Status == "deleting" || asset.Status == "deleted" {
			return command.ErrNotFound
		}
		out = Asset{AssetID: asset.ID, ScanStatus: asset.ScanStatus}
		return nil
	})
	return out, err
}
