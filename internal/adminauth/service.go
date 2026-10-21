// Package adminauth 提供独立后台本地账号、口令及数据库权威会话，不授予学生资格。
package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// 稳定错误用于HTTP边界，不透传数据库、缓存或口令校验细节。
var (
	// ErrInvalid 表示输入或显式技术配置无效。
	ErrInvalid = command.ErrInvalid
	// ErrUnauthenticated 表示当前后台身份无法使用。
	ErrUnauthenticated = command.ErrUnauthenticated
	// ErrForbidden 表示当前权限或旧口令校验不允许该操作。
	ErrForbidden = command.ErrForbidden
	// ErrConflict 表示账号名称或初始化状态冲突。
	ErrConflict = command.ErrConflict
	// ErrUnavailable 表示存储、缓存或资源不可安全使用。
	ErrUnavailable = command.ErrUnavailable
	// ErrRateLimited 表示共享限流或有界口令计算已达到上限。
	ErrRateLimited = errors.New("登录请求过于频繁")
)

// Limiter 必须由装配方提供真实共享限流，故障不能降级为无限登录。
type Limiter interface {
	Allow(context.Context, string) (bool, error)
}

// Config 的所有期限由调用方显式注入，不猜测后台会话或幂等回执保留期。
type Config struct {
	SessionTTL      time.Duration
	Command         command.Config
	InitialPassword string
	ReceiptKey      []byte
}

// Account 是可返回的当前后台投影，不含凭据摘要。
type Account struct {
	ID        string   `json:"id"`
	Version   int64    `json:"version"`
	LoginName string   `json:"login_name"`
	Roles     []string `json:"roles" gorm:"-"`
	Active    bool     `json:"active"`
}

// Session 仅在登录提交成功后返回令牌，数据库只持有其摘要。
type Session struct {
	Token   string  `json:"token"`
	Account Account `json:"account"`
}

// CreateInput 仅供已授权超管创建，不接受客户端设置初始口令。
type CreateInput struct {
	LoginName string   `json:"login_name"`
	Roles     []string `json:"roles"`
	Reason    string   `json:"reason"`
}

// PasswordInput 只在当前请求内持有，不能保存到回执或日志。
type PasswordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// Service 不拥有注入的数据库或缓存，外层须先等待HTTP请求退出再关闭连接池。
type Service struct {
	db      *gorm.DB
	engine  *command.Engine
	config  Config
	limiter Limiter
	logger  *zap.Logger
	dummy   string
	slots   chan struct{}
}

// New 拒绝外部事务和缺失的显式安全参数；构造不创建账号、迁移或修改凭据。
func New(db *gorm.DB, limiter Limiter, logger *zap.Logger, c Config) (*Service, error) {
	if db == nil || db.Statement == nil || limiter == nil || logger == nil || c.SessionTTL <= 0 || c.InitialPassword == "" || len(c.ReceiptKey) < 32 {
		return nil, ErrInvalid
	}
	engine, err := command.New(db, logger, c.Command)
	if err != nil {
		return nil, err
	}
	dummy, err := hashPassword(c.InitialPassword)
	if err != nil {
		return nil, err
	}
	c.ReceiptKey = slices.Clone(c.ReceiptKey)
	return &Service{
		db:      db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}),
		engine:  engine,
		config:  c,
		limiter: limiter,
		logger:  logger,
		dummy:   dummy,
		slots:   make(chan struct{}, 2),
	}, nil
}

func (s *Service) complete(operation, requestID string, start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	// 请求标识来自HTTP中间件，调用库时也只接受相同的安全字符集，避免自由输入进入日志。
	if len(requestID) > 128 || strings.ContainsFunc(requestID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) {
		requestID = ""
	}
	s.logger.Info("后台身份操作完成", zap.String("operation", operation), zap.String("request_id", requestID), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

func normalize(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrInvalid, ErrUnauthenticated, ErrForbidden, ErrConflict, ErrRateLimited, command.ErrIdempotencyConflict, command.ErrNotFound, command.ErrInternal} {
		if errors.Is(err, known) {
			return known
		}
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "23505" {
		return ErrConflict
	}
	return ErrUnavailable
}

func (s *Service) passwordSlot(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ErrUnavailable
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, nil
	default:
		return nil, ErrRateLimited
	}
}

// Bootstrap 仅用于离线显式初始化；任一后台账号已存在就拒绝，不接管历史账号或重置口令。
func (s *Service) Bootstrap(ctx context.Context, loginName string) (out Account, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		if err != nil {
			out = Account{}
		}
		s.complete("bootstrap", "", start, err)
	}()
	if strings.TrimSpace(loginName) == "" {
		return out, ErrInvalid
	}
	release, err := s.passwordSlot(ctx)
	if err != nil {
		return out, err
	}
	defer release()
	hash, err := hashPassword(s.config.InitialPassword)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := s.lockTimeout(ctx, db); e != nil {
			return e
		}
		// 只串行化显式初始化，不在普通登录或启动中取得此维护锁。
		if e := bootstrapAvailable(ctx, db); e != nil {
			return e
		}
		id := uuid.NewString()
		if e := insertLocal(ctx, db, id, loginName, hash); e != nil {
			return e
		}
		if e := insertRole(ctx, db, id, "super_admin"); e != nil {
			return e
		}
		if e := audit(ctx, db, id, id, "bootstrap", "显式离线初始化"); e != nil {
			return e
		}
		var e error
		out, e = projection(ctx, db, id)
		return e
	})
	if err != nil {
		out = Account{}
	}
	return out, err
}

// Create 复用当前数据库授权和根命令回执；重放仍要求当前超管权限。
func (s *Service) Create(ctx context.Context, token, key, requestID string, input CreateInput) (out Account, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		if err != nil {
			out = Account{}
		}
		s.complete("create", requestID, start, err)
	}()
	if !validKey(key) || input.Roles == nil || strings.TrimSpace(input.LoginName) == "" || strings.TrimSpace(input.Reason) == "" {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	if err = precheckSession(ctx, s.db, token); err != nil {
		return out, err
	}
	input.Roles = slices.Clone(input.Roles)
	slices.Sort(input.Roles)
	input.Roles = slices.Compact(input.Roles)
	for _, role := range input.Roles {
		if !slices.Contains([]string{"review_operator", "crisis_operator", "super_admin"}, role) {
			return out, ErrInvalid
		}
	}
	release, err := s.passwordSlot(ctx)
	if err != nil {
		return out, err
	}
	defer release()
	hash, err := hashPassword(s.config.InitialPassword)
	if err != nil {
		return out, err
	}
	definition := command.Definition{
		Kind:         "admin.account.create",
		Method:       "POST",
		ResourcePath: "/api/v1/admin/accounts",
		ActorKind:    "admin",
		AllowedRoles: []string{"super_admin"},
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(context.Context, *command.Tx) error { return nil },
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			id := uuid.NewString()
			if e := insertLocal(ctx, tx.DB(), id, input.LoginName, hash); e != nil {
				return "", e
			}
			for _, role := range input.Roles {
				if e := insertRole(ctx, tx.DB(), id, role); e != nil {
					return "", e
				}
			}
			return id, audit(ctx, tx.DB(), tx.Actor().ID, id, "create", input.Reason)
		},
		Resolve: func(ctx context.Context, tx *command.Tx, id string) error {
			var e error
			out, e = projection(ctx, tx.DB(), id)
			return e
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: requestID, Request: input})
	return out, err
}

type credential struct {
	ID           string
	PasswordHash string
	AuthEpoch    int64
	Status       string
}

// Login 先做共享限流，再在事务外校验口令；持账号锁后重读摘要，避免改密后签发旧口令会话。
func (s *Service) Login(ctx context.Context, loginName, password, source, requestID string) (out Session, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		if err != nil {
			out = Session{}
		}
		s.complete("login", requestID, start, err)
	}()
	if strings.TrimSpace(loginName) == "" || password == "" || source == "" {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	for _, scope := range []string{"account\x00" + loginName, "source\x00" + source} {
		allowed, e := s.limiter.Allow(ctx, hex.EncodeToString(keyedDigest(s.config.ReceiptKey, scope)))
		if e != nil {
			return out, ErrUnavailable
		}
		if !allowed {
			return out, ErrRateLimited
		}
	}
	release, err := s.passwordSlot(ctx)
	if err != nil {
		return out, err
	}
	defer release()
	var rows []credential
	if e := findByName(ctx, s.db, loginName, &rows); e != nil {
		return out, e
	}
	hash := s.dummy
	if len(rows) == 1 && rows[0].PasswordHash != "" {
		hash = rows[0].PasswordHash
	}
	valid := verifyPassword(hash, password)
	if !valid || len(rows) != 1 || rows[0].PasswordHash == "" || rows[0].Status != "active" {
		return out, ErrUnauthenticated
	}
	previous := rows[0]
	var token [32]byte
	if _, e := rand.Read(token[:]); e != nil {
		return out, ErrUnavailable
	}
	raw := base64.RawURLEncoding.EncodeToString(token[:])
	digest := sha256.Sum256([]byte(raw))
	clear(token[:])
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := s.lockTimeout(ctx, db); e != nil {
			return e
		}
		if e := lockAdmin(ctx, db, previous.ID); e != nil {
			return e
		}
		var current []credential
		if e := findByID(ctx, db, previous.ID, &current); e != nil {
			return e
		}
		if len(current) != 1 || current[0].Status != "active" || current[0].PasswordHash != previous.PasswordHash || current[0].AuthEpoch != previous.AuthEpoch {
			return ErrUnauthenticated
		}
		var now time.Time
		if e := databaseNow(ctx, db, &now); e != nil {
			return e
		}
		if e := insertSession(ctx, db, previous.ID, digest[:], current[0].AuthEpoch, now, now.Add(s.config.SessionTTL)); e != nil {
			return e
		}
		var e error
		out.Account, e = projection(ctx, db, previous.ID)
		return e
	})
	if err == nil {
		out.Token = raw
	}
	return out, err
}

func validKey(key string) bool {
	return len(key) >= 1 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}

// ChangePassword 无角色账号也可修改本人口令；回执摘要采用独立密钥HMAC，不留下可离线猜测的口令请求摘要。
func (s *Service) ChangePassword(ctx context.Context, token, key, requestID string, input PasswordInput) (err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		s.complete("password_change", requestID, start, err)
	}()
	if !validKey(key) || input.CurrentPassword == "" || input.NewPassword == "" {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	if err = precheckSession(ctx, s.db, token); err != nil {
		return err
	}
	release, err := s.passwordSlot(ctx)
	if err != nil {
		return err
	}
	defer release()
	// 新摘要提前在事务外生成；当前口令在锁后验证，防止检查与更新之间被并发改密穿透。
	next, err := hashPassword(input.NewPassword)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(input)
	defer clear(body)
	requestDigest := keyedDigest(s.config.ReceiptKey, "password_request\x00"+string(body))
	keyDigest := sha256.Sum256([]byte(key))
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := s.lockTimeout(ctx, db); e != nil {
			return e
		}
		actor, now, e := s.authorized(ctx, db, token)
		if e != nil {
			return e
		}
		var receipts []passwordReceipt
		const scope = "admin.password.change"
		if e = findReceipt(ctx, db, actor.ID, scope, keyDigest[:], &receipts); e != nil {
			return e
		}
		if len(receipts) == 1 && receipts[0].ExpiresAt.After(now) {
			if !slices.Equal(receipts[0].RequestHash, requestDigest) {
				return command.ErrIdempotencyConflict
			}
			return nil
		}
		if !verifyPassword(actor.PasswordHash, input.CurrentPassword) {
			return ErrForbidden
		}
		if e = replacePassword(ctx, db, actor.ID, next, now); e != nil {
			return e
		}
		if e = audit(ctx, db, actor.ID, actor.ID, "password_change", "本人修改口令并撤销旧会话"); e != nil {
			return e
		}
		return saveReceipt(ctx, db, actor.ID, scope, keyDigest[:], requestDigest, now.Add(s.config.Command.ReceiptTTL))
	})
}

// Logout 先复核当前会话再撤销；被撤销的旧token不能重放成有效身份。
func (s *Service) Logout(ctx context.Context, token, key, requestID string) (err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		s.complete("logout", requestID, start, err)
	}()
	if !validKey(key) {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := s.lockTimeout(ctx, db); e != nil {
			return e
		}
		_, now, e := s.authorized(ctx, db, token)
		if e != nil {
			return e
		}
		hash := sha256.Sum256([]byte(token))
		return revokeSession(ctx, db, hash[:], now)
	})
}
