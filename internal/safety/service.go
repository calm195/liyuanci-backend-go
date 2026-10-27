// Package safety 提供拉黑、举报、处罚、警告和申诉的事务边界。
package safety

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

// Config 显式提供事务、独立用途密钥与文字传输字节预算。
type Config struct {
	Command      command.Config
	Key          []byte
	MaxTextBytes int
}

// Service 不拥有连接池或密钥服务；所有写入由统一命令框架提交。
type Service struct {
	db     *gorm.DB
	engine *command.Engine
	codec  *securedata.Codec
	logger *zap.Logger
	cfg    Config
}

// DeriveKey 从私有根密钥派生安全治理独立摘要和游标密钥。
func DeriveKey(root []byte) []byte {
	m := hmac.New(sha256.New, root)
	m.Write([]byte("safety.scope.v1"))
	return m.Sum(nil)
}

// New 只构造服务，不加载默认举报类型、不迁移、不启动任务。
func New(db *gorm.DB, logger *zap.Logger, codec *securedata.Codec, cfg Config) (*Service, error) {
	if codec == nil || len(cfg.Key) < 32 || cfg.MaxTextBytes < 1 {
		return nil, command.ErrInvalid
	}
	engine, e := command.New(db, logger, cfg.Command)
	if e != nil {
		return nil, e
	}
	cfg.Key = append([]byte(nil), cfg.Key...)
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), engine: engine, codec: codec, logger: logger, cfg: cfg}, nil
}

// Input 是各固定命令的输入集合，处理器按路由严格限制允许字段。
// 命令名来自服务端路由映射，绝不接受客户端表名、权限或回调。
type Input struct {
	TargetUserID    string `json:"target_user_id,omitempty"`
	TargetKind      string `json:"target_kind,omitempty"`
	ID              string `json:"id,omitempty"`
	TypeCode        string `json:"type_code,omitempty"`
	Description     string `json:"description,omitempty"`
	PenaltyID       string `json:"penalty_id,omitempty"`
	Body            string `json:"body,omitempty"`
	ViolationRef    string `json:"violation_ref,omitempty"`
	ContentRef      string `json:"content_ref,omitempty"`
	ContentVersion  int64  `json:"content_version,omitempty"`
	Level           string `json:"level,omitempty"`
	Decision        string `json:"decision,omitempty"`
	Reason          string `json:"reason,omitempty"`
	ExpectedVersion int64  `json:"expected_version,omitempty"`
}

// Query 是普通列表的主体和用途绑定游标。
type Query struct {
	Cursor string
	Limit  int
}

// Summary 不含举报人身份、正文、学号或私聊摘要。
type Summary struct {
	ID             string `json:"id"`
	Version        int64  `json:"version"`
	Status         string `json:"status"`
	Kind           string `json:"kind,omitempty"`
	ContentVersion int64  `json:"content_version,omitempty"`
}

// BlockEntry 只返回本人拉黑目标及条目版本，解除时不能使用Me版本。
type BlockEntry struct {
	TargetUserID string `json:"target_user_id"`
	Version      int64  `json:"version"`
}

// Page 使用当前授权筛选后的队列，允许空页。
type Page struct {
	Items      []any   `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// Warning 的所有时刻均由数据库提供，不接受客户端时钟。
type Warning struct {
	ID               string     `json:"id"`
	Version          int64      `json:"version"`
	Notice           string     `json:"notice"`
	FirstDisplayedAt *time.Time `json:"first_displayed_at"`
	ConfirmableAt    *time.Time `json:"confirmable_at"`
	ServerNow        time.Time  `json:"server_now"`
	RemainingSeconds int        `json:"remaining_seconds"`
	AcknowledgedAt   *time.Time `json:"acknowledged_at"`
}

// Appeal 是本人或授权后台可见的最小申诉结论。
type Appeal struct {
	ID      string  `json:"id"`
	Version int64   `json:"version"`
	Status  string  `json:"status"`
	Reason  *string `json:"reason,omitempty"`
}

func validID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && u != uuid.Nil && u.String() == id
}
func (s *Service) text(v string, required bool) bool {
	return utf8.ValidString(v) && len(v) <= s.cfg.MaxTextBytes && (!required || strings.TrimSpace(v) != "")
}
func (s *Service) digest(in any) []byte {
	b, _ := json.Marshal(in)
	defer clear(b)
	m := hmac.New(sha256.New, s.cfg.Key)
	m.Write(b)
	return m.Sum(nil)
}
func (s *Service) done(op, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	if *err != nil {
		known := false
		for _, e := range []error{command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrConflict, command.ErrWarningWait, command.ErrRetry, command.ErrInternal} {
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
	fields := []zap.Field{zap.String("operation", op), zap.String("result", result), zap.Duration("duration", time.Since(start))}
	if len(rid) <= 128 && !strings.ContainsAny(rid, "\r\n") {
		fields = append(fields, zap.String("request_id", rid))
	}
	s.logger.Info("安全治理操作完成", fields...)
}
func found(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return command.ErrNotFound
	}
	return err
}
func (s *Service) encrypt(ctx context.Context, purpose, id string, v any) (json.RawMessage, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	defer clear(b)
	env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: 1}, b)
	if e != nil {
		return nil, e
	}
	return json.Marshal(env)
}
func (s *Service) decrypt(ctx context.Context, purpose, id string, b json.RawMessage) (string, error) {
	var env securedata.Envelope
	if json.Unmarshal(b, &env) != nil {
		return "", command.ErrUnavailable
	}
	raw, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: 1}, env)
	if e != nil {
		return "", e
	}
	defer clear(raw)
	var out string
	e = json.Unmarshal(raw, &out)
	return out, e
}
func audit(db *gorm.DB, actor, subject, purpose string) error {
	return db.Create(&entity.AccessAudit{ID: uuid.NewString(), ActorID: actor, SubjectRef: subject, Purpose: purpose, Outcome: "allowed"}).Error
}
func notify(db *gorm.DB, account, kind, id string) error {
	return db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: account, Type: kind, TargetRef: id}).Error
}

// read 使用一致快照重验会话。后台读取在同一快照复核当前后台角色。
// 普通案件投影不包含危机身份或学生身份密文。
func (s *Service) read(ctx context.Context, token string, admin bool, run func(*gorm.DB, string, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		if token == "" {
			return command.ErrUnauthenticated
		}
		h := sha256.Sum256([]byte(token))
		var ids []string
		q := db.Table("public.sessions AS s").Select("s.subject_id")
		if admin {
			q = q.Joins("JOIN public.admin_accounts a ON a.id=s.admin_id").Where("s.subject_kind='admin' AND a.status='active'")
		} else {
			q = q.Joins("JOIN public.accounts a ON a.id=s.account_id").Where("s.subject_kind='student' AND a.lifecycle='active'")
		}
		if e := q.Where("s.token_hash=? AND s.auth_epoch=a.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>?", h[:], now).Scan(&ids).Error; e != nil {
			return e
		}
		if len(ids) != 1 {
			return command.ErrUnauthenticated
		}
		if admin {
			if _, e := command.LockPlan(ctx, db, command.Discovery{Admins: ids}, s.cfg.Command.LockTimeout); e != nil {
				return e
			}
			var session entity.Session
			if e := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash=? AND subject_kind='admin' AND revoked_at IS NULL AND expires_at>?", h[:], now).Take(&session).Error; e != nil {
				return found(e)
			}
			var roles []string
			if e := db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", ids[0]).Pluck("role", &roles).Error; e != nil {
				return e
			}
			if !slices.ContainsFunc(roles, func(r string) bool {
				return slices.Contains([]string{"review_operator", "crisis_operator", "super_admin"}, r)
			}) {
				return command.ErrForbidden
			}
		}
		return run(db, ids[0], now)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}
func (s *Service) cursor(actor, op string, at time.Time, id string) string {
	payload := actor + "|" + op + "|" + at.UTC().Format(time.RFC3339Nano) + "|" + id
	m := hmac.New(sha256.New, s.cfg.Key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (s *Service) after(actor, op, c string) (time.Time, string, error) {
	if c == "" {
		return time.Time{}, "", nil
	}
	if len(c) > 1024 {
		return time.Time{}, "", command.ErrInvalid
	}
	parts := strings.Split(c, ".")
	if len(parts) != 2 {
		return time.Time{}, "", command.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return time.Time{}, "", command.ErrInvalid
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	p := strings.Split(string(b), "|")
	m := hmac.New(sha256.New, s.cfg.Key)
	m.Write(b)
	if e != nil || len(p) != 4 || p[0] != actor || p[1] != op || !validID(p[3]) || !hmac.Equal(sig, m.Sum(nil)) {
		return time.Time{}, "", command.ErrInvalid
	}
	at, e := time.Parse(time.RFC3339Nano, p[2])
	if e != nil {
		return time.Time{}, "", command.ErrInvalid
	}
	return at, p[3], nil
}
