// Package chat 提供原参与者私聊、最终送达序号及站内通知，不承担外部通知发送。
package chat

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Config 显式注入技术字节预算；预算不是产品字符数限制。
type Config struct {
	Command      command.Config
	Key          []byte
	MaxBodyBytes int
}

// Service 不拥有数据库及密钥生命周期，命令事务内不调用通知网络。
type Service struct {
	db     *gorm.DB
	engine *command.Engine
	codec  *securedata.Codec
	logger *zap.Logger
	cfg    Config
}

// DeriveKey 从已有私有根密钥隔离聊天摘要和游标用途。
func DeriveKey(root []byte) []byte {
	m := hmac.New(sha256.New, root)
	m.Write([]byte("chat.scope.v1"))
	return m.Sum(nil)
}

// New 仅构造服务，不迁移或启动后台工作。
func New(db *gorm.DB, logger *zap.Logger, codec *securedata.Codec, cfg Config) (*Service, error) {
	if codec == nil || len(cfg.Key) < 32 || cfg.MaxBodyBytes < 1 {
		return nil, command.ErrInvalid
	}
	e, err := command.New(db, logger, cfg.Command)
	if err != nil {
		return nil, err
	}
	cfg.Key = append([]byte(nil), cfg.Key...)
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), engine: e, codec: codec, logger: logger, cfg: cfg}, nil
}
func (s *Service) done(op, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	if *err != nil {
		known := false
		for _, e := range []error{command.ErrInvalid, command.ErrNotFound, command.ErrForbidden, command.ErrUnauthenticated, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrConflict, command.ErrBlockedInteraction, command.ErrRetry, command.ErrInternal} {
			if errors.Is(*err, e) {
				*err = e
				known = true
				break
			}
		}
		if !known {
			*err = command.ErrUnavailable
		}
	}
	result := "success"
	if *err != nil {
		result = "failure"
	}
	s.logger.Info("聊天通知操作完成", zap.String("operation", op), zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

// MessageInput 不允许客户端指定作者、审核状态或送达序号。
type MessageInput struct {
	ClientMessageID    string `json:"client_message_id"`
	Body               string `json:"body"`
	ExpectedWriteEpoch int64  `json:"expected_write_epoch"`
}

// Accepted 仅确认持久接收；当前结果通过本人消息状态读取。
type Accepted struct {
	MessageID   string `json:"message_id"`
	Status      string `json:"status"`
	DeliverySeq *int64 `json:"delivery_seq"`
}

func validID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && u.String() == id && u != uuid.Nil
}
func (s *Service) digest(in MessageInput) []byte {
	b, _ := json.Marshal(in)
	defer clear(b)
	m := hmac.New(sha256.New, s.cfg.Key)
	m.Write([]byte("chat.submit\x00"))
	m.Write(b)
	return m.Sum(nil)
}

// Send 将原文信封密文、双方代次、审核绑定和持久工作原子提交，永不提前分配送达号。
func (s *Service) Send(ctx context.Context, token, key, rid, id string, in MessageInput) (out Accepted, err error) {
	defer func() {
		if err != nil {
			out = Accepted{}
		}
	}()
	defer s.done("message.submit", rid, time.Now(), &err)
	if !validID(id) || !utf8.ValidString(in.Body) || strings.TrimSpace(in.Body) == "" || len(in.Body) > s.cfg.MaxBodyBytes || in.ClientMessageID == "" || !utf8.ValidString(in.ClientMessageID) || len(in.ClientMessageID) > 256 || in.ExpectedWriteEpoch < 0 {
		return out, command.ErrInvalid
	}
	messageID, registryID, taskID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	digest := s.digest(in)
	d := command.Definition{Kind: "message.submit", Method: "POST", ResourcePath: "/api/v1/conversations/" + id + "/messages", ActorKind: "student", Access: command.Verified}
	d.Discover = func(ctx context.Context, db *gorm.DB, actor command.Subject) (command.Discovery, error) {
		p, e := DiscoverConversation(ctx, db, id)
		if e != nil {
			return p, e
		}
		var rows []entity.Message
		if e = db.WithContext(ctx).Where("conversation_id=? AND sender_id=? AND client_message_id=?", id, actor.ID, in.ClientMessageID).Find(&rows).Error; e != nil {
			return p, e
		}
		for _, m := range rows {
			p.Resources = append(p.Resources, command.Resource{Kind: command.Message, ID: m.ID})
			if m.ReviewTaskID != nil {
				p.Resources = append(p.Resources, command.Resource{Kind: command.ModerationTask, ID: *m.ReviewTaskID})
			}
		}
		return p, nil
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		v, e := LoadConversation(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		if !v.CanRead(tx.Actor().ID) {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		var rows []entity.Message
		if e := db.Where("conversation_id=? AND sender_id=? AND client_message_id=?", id, tx.Actor().ID, in.ClientMessageID).Find(&rows).Error; e != nil {
			return "", e
		}
		if len(rows) > 0 {
			if !hmac.Equal(rows[0].RequestDigest, digest) {
				return "", command.ErrIdempotencyConflict
			}
			return rows[0].ID, nil
		}
		v, e := LoadConversation(ctx, db, id)
		if e != nil {
			return "", e
		}
		if e = Writable(ctx, db, v, tx.Now()); e != nil {
			return "", e
		}
		if v.Conversation.WriteEpoch != in.ExpectedWriteEpoch {
			return "", command.ErrVersionConflict
		}
		data, _ := json.Marshal(in.Body)
		defer clear(data)
		envelope, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "chat_message", ObjectID: messageID, Version: 1}, data)
		if e != nil {
			return "", e
		}
		raw, e := json.Marshal(envelope)
		if e != nil {
			return "", e
		}
		peer := v.Requester
		if peer.ID == tx.Actor().ID {
			peer = v.Helper
		}
		hold, e := holdEpoch(ctx, db, v.Wish.ID)
		if e != nil {
			return "", e
		}
		m := entity.Message{ID: messageID, ConversationID: id, SenderID: tx.Actor().ID, ClientMessageID: in.ClientMessageID, BodyCipher: raw, ContentVersion: 1, CancelEpoch: v.Conversation.WriteEpoch, PeerEpoch: &peer.AuthEpoch, ReviewTaskID: &taskID, RequestDigest: digest, CreatedAt: tx.Now()}
		if e = db.Create(&m).Error; e != nil {
			return "", e
		}
		if e = db.Create(&entity.ContentRegistry{ID: registryID, Kind: "message", EntityID: messageID, AuthorID: m.SenderID, ContentVersion: 1, SchoolDetectionVersion: "pending"}).Error; e != nil {
			return "", e
		}
		epoch := v.Wish.RestrictionEpoch
		if e = db.Create(&entity.ModerationTask{ID: taskID, ContentID: registryID, ContentVersion: 1, SubjectEpoch: tx.Actor().AuthEpoch, RestrictionEpoch: &epoch, HoldEpoch: hold, CreatedAt: tx.Now()}).Error; e != nil {
			return "", e
		}
		// 消息先写入时审核任务尚不存在；外键使用延迟约束以维持同一原子提交。
		if e = durable.Append(ctx, db, s.logger, wish.ReviewRoute(), durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: taskID, AggregateVersion: 1}); e != nil {
			return "", e
		}
		return messageID, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var m entity.Message
		if e := tx.DB().WithContext(ctx).Where("id=? AND conversation_id=? AND sender_id=?", ref, id, tx.Actor().ID).Take(&m).Error; e != nil {
			return command.ErrNotFound
		}
		out = Accepted{MessageID: ref, Status: "pending_scan"}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: digest})
	return
}
