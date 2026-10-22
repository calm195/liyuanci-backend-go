package wish

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// ServiceConfig 要求显式配置命令期限与独立回执摘要密钥，不使用默认生产密钥。
type ServiceConfig struct {
	// MaxStoryBytes 为正时显式启用故事及分享，字节预算不是业务字数限制。
	MaxStoryBytes int
	Command       command.Config
	ReceiptKey    []byte
}

// CreatedDraft 仅返回当前可访问结果的技术元数据，不回放原文或历史权限。
type CreatedDraft struct {
	ID             string
	Version        int64
	ContentVersion int64
	State          State
}

// Service 不拥有数据库、密钥或字典生命周期；写入全部由命令引擎管理根事务。
type Service struct {
	db         *gorm.DB
	config     ServiceConfig
	engine     *command.Engine
	policy     *Policy
	codec      *securedata.Codec
	receiptKey []byte
}

// NewService 仅构造显式注入的心愿服务，不注册路由、不执行迁移。
func NewService(db *gorm.DB, logger *zap.Logger, codec *securedata.Codec, grades Grades, cfg ServiceConfig) (*Service, error) {
	if codec == nil || len(cfg.ReceiptKey) < 32 || cfg.MaxStoryBytes < 0 {
		return nil, command.ErrInvalid
	}
	engine, err := command.New(db, logger, cfg.Command)
	if err != nil {
		return nil, err
	}
	policy, err := NewPolicy(logger, grades)
	if err != nil {
		return nil, err
	}
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), config: cfg, engine: engine, policy: policy, codec: codec, receiptKey: append([]byte(nil), cfg.ReceiptKey...)}, nil
}

func (s *Service) requestDigest(input ContentInput) (string, error) {
	// JSON 会替换非法 UTF-8；摘要前拒绝，防止非法输入与合法替换字符共享成功回执。
	values := []string{input.Category, input.Body, input.Campus}
	if input.RewardMethod != nil {
		values = append(values, *input.RewardMethod)
	}
	if input.IdentityTags != nil {
		for _, value := range []*string{input.IdentityTags.Campus, input.IdentityTags.Grade, input.IdentityTags.Gender} {
			if value != nil {
				values = append(values, *value)
			}
		}
	}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return "", command.ErrInvalid
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", command.ErrInvalid
	}
	defer clear(encoded)
	mac := hmac.New(sha256.New, s.receiptKey)
	_, _ = mac.Write([]byte("wish.create\x00"))
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Create 在当前账号 guard 下原子写入心愿、加密内容版本、注册表和幂等回执。
// 字典与密钥只在当前会话/资格复核后访问；重放重新查询当前归属，不重建或解密历史内容。
func (s *Service) Create(ctx context.Context, token, key, requestID string, input ContentInput) (out CreatedDraft, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		if err != nil {
			out = CreatedDraft{}
		}
		s.policy.finish("心愿创建操作完成", "create_draft", requestID, start, err)
	}()
	if len(key) == 0 || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
		return out, command.ErrInvalid
	}
	digest, err := s.requestDigest(input)
	if err != nil {
		return out, err
	}
	// 标识在本次调用及数据库重试间固定；有效回执会解析原结果而不是使用新标识。
	id, registryID := uuid.NewString(), uuid.NewString()
	d := command.Definition{
		Kind: "wish.create", Method: "POST", ResourcePath: "/api/v1/wishes",
		ActorKind: "student", Access: command.Verified,
	}
	d.Discover = func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
		return command.Discovery{}, nil
	}
	d.Authorize = func(context.Context, *command.Tx) error { return nil }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		guard, ok := tx.Guard(tx.Actor().ID)
		if !ok {
			return "", command.ErrInternal
		}
		plan, failure := s.policy.CreateDraft(ctx, requestID, Eligibility{
			Active: guard.Lifecycle == "active", Verified: guard.Verification == "verified",
		}, input)
		if failure != nil {
			return "", failure
		}
		// 未审标签与正文一起加密；公开列保持空值，不能把用户输入写成已审公开投影。
		plain, failure := json.Marshal(plan.Content)
		if failure != nil {
			return "", command.ErrInternal
		}
		defer clear(plain)
		envelope, failure := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: id, Version: 1}, plain)
		if failure != nil {
			return "", command.ErrUnavailable
		}
		cipher, failure := json.Marshal(envelope)
		if failure != nil {
			return "", command.ErrInternal
		}
		if failure = createDraftRows(ctx, tx.DB(), tx.Now(), id, registryID, tx.Actor().ID, plan, cipher); failure != nil {
			return "", failure
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var failure error
		out, failure = resolveCreated(ctx, tx.DB(), ref, tx.Actor().ID)
		return failure
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: requestID, Request: digest})
	return out, err
}
