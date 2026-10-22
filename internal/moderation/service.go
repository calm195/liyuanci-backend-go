// Package moderation 管理不可变词库、当前版本扫描和案件级人工复核。
package moderation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// Config 显式提供技术预算和独立游标密钥，不隐含生产规则。
type Config struct {
	Command    command.Config
	CursorKey  []byte
	MaxEntries int
}

// Service 使用独立后台连接池，不拥有数据库或密钥服务生命周期。
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
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), engine: engine, logger: log, codec: codec, cfg: cfg}, nil
}
func validID(id string) bool { u, e := uuid.Parse(id); return e == nil && u.String() == id }
func kind(k string) bool {
	return slices.Contains([]string{"crisis", "school", "ordinary_violation"}, k)
}
func safe(err error) error {
	for _, e := range []error{command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrConflict, command.ErrRetry, command.ErrInternal} {
		if errors.Is(err, e) {
			return e
		}
	}
	if err != nil {
		return command.ErrUnavailable
	}
	return nil
}
func (s *Service) done(op, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	*err = safe(*err)
	result := "success"
	if *err != nil {
		result = "failure"
	}
	fields := []zap.Field{zap.String("operation", op), zap.String("result", result), zap.Duration("duration", time.Since(start))}
	if len(rid) <= 128 && !strings.ContainsAny(rid, "\r\n") {
		fields = append(fields, zap.String("request_id", rid))
	}
	s.logger.Info("内容审核操作完成", fields...)
}
func (s *Service) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	bounded, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(bounded).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}

// authorize 锁定当前后台主体及会话，每次复核数据库角色，学生和危机单独角色不能进入普通审核。
func (s *Service) authorize(ctx context.Context, db *gorm.DB, token string, plan command.Discovery) (string, error) {
	if token == "" {
		return "", command.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var session entity.Session
	if e := db.Where("token_hash=? AND subject_kind='admin'", hash[:]).Take(&session).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return "", command.ErrUnauthenticated
		}
		return "", e
	}
	if session.AdminID == nil {
		return "", command.ErrUnauthenticated
	}
	id := *session.AdminID
	plan.Admins = append(plan.Admins, id)
	tx, e := command.LockPlan(ctx, db, plan, s.cfg.Command.LockTimeout)
	if e != nil {
		return "", e
	}
	var admin entity.AdminAccount
	if e = db.Where("id=?", id).Take(&admin).Error; e != nil {
		return "", e
	}
	if admin.Status != "active" {
		return "", command.ErrUnauthenticated
	}
	if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", session.ID).Take(&session).Error; e != nil {
		return "", e
	}
	if session.RevokedAt != nil || session.AuthEpoch != admin.AuthEpoch || !session.ExpiresAt.After(tx.Now()) {
		return "", command.ErrUnauthenticated
	}
	var roles []string
	if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", id).Pluck("role", &roles).Error; e != nil {
		return "", e
	}
	if !slices.Contains(roles, "super_admin") && !slices.Contains(roles, "review_operator") {
		return "", command.ErrForbidden
	}
	return id, nil
}
func audit(db *gorm.DB, actor, subject, purpose string) error {
	return db.Create(&entity.AccessAudit{ID: uuid.NewString(), ActorID: actor, SubjectRef: subject, Purpose: purpose, Outcome: "allowed"}).Error
}
func (s *Service) cursor(actor, purpose, id string) string {
	payload := actor + "|" + purpose + "|" + id
	mac := hmac.New(sha256.New, s.cfg.CursorKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(append([]byte(payload+"|"), mac.Sum(nil)...))
}
func (s *Service) after(actor, purpose, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 512 {
		return "", command.ErrInvalid
	}
	raw, e := base64.RawURLEncoding.DecodeString(cursor)
	if e != nil || len(raw) < 33 {
		return "", command.ErrInvalid
	}
	payload, signature := raw[:len(raw)-33], raw[len(raw)-32:]
	parts := strings.Split(string(payload), "|")
	mac := hmac.New(sha256.New, s.cfg.CursorKey)
	mac.Write(payload)
	if raw[len(raw)-33] != '|' || len(parts) != 3 || parts[0] != actor || parts[1] != purpose || !validID(parts[2]) || !hmac.Equal(signature, mac.Sum(nil)) {
		return "", command.ErrInvalid
	}
	return parts[2], nil
}
func text(s string, max int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
func (s *Service) digest(value any) string {
	b, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, s.cfg.CursorKey)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
