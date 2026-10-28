// Package studentauth 提供可注入微信身份交换及数据库权威学生会话，不授予隐含认证资格。
package studentauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	identitydata "github.com/calm195/liyuanci-backend-go/internal/identity"
)

// Exchanger 只向服务层返回微信OpenID，不返回或保留供应商会话密钥；实现必须遵守context且禁止记录code/身份。
type Exchanger interface {
	Exchange(context.Context, string) ([]byte, error)
}

// Resolver 在传入事务内先取身份全局锁，再锁账号，返回账号引用；失败不得留下半个绑定。
type Resolver interface {
	ResolveOpenID(context.Context, *gorm.DB, []byte) (string, error)
}

// Limiter 是共享故障关闭的登录资源准入，不代替数据库会话与资格授权。
type Limiter interface {
	Allow(context.Context, string) (bool, error)
}

// Config 必须显式给出期限和独立会话派生密钥，不套用后台会话参数。
type Config struct {
	// Safety 仅在安全治理路由全部成功装配时提示受限申诉及警告操作。
	// DeletionRequests只在显式装配注销入口后提示受罚本人可用的隐私动作。
	DeletionRequests bool
	Safety           bool
	// Wishes 只在本域全部入口已成功装配时提示创建动作。
	Wishes bool
	// StudentCardAssets 仅在同一装配显式注册学生卡路由后启用动作提示。
	StudentCardAssets bool
	// VerificationRequests 仅在人工认证服务和路由已显式装配后启用申请动作。
	VerificationRequests bool
	SessionTTL           time.Duration
	ExchangeTTL          time.Duration
	Command              command.Config
	SessionKey           []byte
}

// Me 仅包含本人非敏感资料及当前可用操作，不回显完整身份字段。
type Me struct {
	ID               string   `json:"id"`
	Version          int64    `json:"version"`
	Nickname         string   `json:"nickname,omitempty"`
	Verification     string   `json:"verification"`
	HideIdentityTags bool     `json:"hide_identity_tags"`
	AllowedActions   []string `json:"allowed_actions" gorm:"-"`
	PenaltyNotice    string   `json:"penalty_notice,omitempty" gorm:"-"`
}

// Session 只在会话事务提交后返回；数据库只保存摘要及独立随机派生材料。
type Session struct {
	Token string `json:"token"`
	Me    Me     `json:"me"`
}

// ProfileInput 不允许修改学号、认证状态或角色。
type ProfileInput struct {
	Nickname         string `json:"nickname"`
	HideIdentityTags *bool  `json:"hide_identity_tags"`
	ExpectedVersion  int64  `json:"expected_version"`
}

// Service 不拥有连接池或适配器；外层先停止HTTP请求，再关闭注入资源。
type Service struct {
	db       *gorm.DB
	engine   *command.Engine
	logger   *zap.Logger
	exchange Exchanger
	identity Resolver
	limiter  Limiter
	config   Config
}

// New 仅构造显式服务，不初始化检索密钥、执行迁移或创建默认账号。
func New(db *gorm.DB, logger *zap.Logger, exchange Exchanger, identity Resolver, limiter Limiter, cfg Config) (*Service, error) {
	if logger == nil || exchange == nil || identity == nil || limiter == nil || len(cfg.SessionKey) < 32 || cfg.SessionTTL < time.Millisecond || cfg.ExchangeTTL < time.Millisecond || cfg.ExchangeTTL > cfg.SessionTTL {
		return nil, command.ErrInvalid
	}
	engine, err := command.New(db, logger, cfg.Command)
	if err != nil {
		return nil, err
	}
	cfg.SessionKey = slices.Clone(cfg.SessionKey)
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), engine: engine, logger: logger, exchange: exchange, identity: identity, limiter: limiter, config: cfg}, nil
}

func (s *Service) digest(scope string, value []byte) []byte {
	mac := hmac.New(sha256.New, s.config.SessionKey)
	_, _ = mac.Write([]byte(scope + "\x00"))
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func (s *Service) complete(operation, requestID string, start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	if len(requestID) > 128 || strings.ContainsFunc(requestID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) {
		requestID = ""
	}
	s.logger.Info("学生身份操作完成", zap.String("operation", operation), zap.String("request_id", requestID), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

func safe(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrConflict, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrInternal, ErrRateLimited} {
		if errors.Is(err, known) {
			return known
		}
	}
	return command.ErrUnavailable
}

// ErrRateLimited 表示共享登录准入达到限制，不能缓存故障放行。
var ErrRateLimited = errors.New("登录请求过于频繁")

func validKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}

// Login 同code只调用一次交换适配器；无有效回执、失败或中断时要求客户端获取新code。
// 网络交换不持有数据库事务；未提交身份/会话不会返回token，重复请求只读取当前有效会话。
func (s *Service) Login(ctx context.Context, code, clientID, source, requestID string) (out Session, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		if err != nil {
			out = Session{}
		}
		s.complete("login", requestID, start, err)
	}()
	if strings.TrimSpace(code) == "" || len(code) > 4096 || !validKey(clientID) || source == "" {
		return out, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	// code、来源和客户端请求标识只以用途隔离HMAC进入技术存储或限流键。
	for _, scope := range []struct{ name, value string }{{"source", source}, {"code", code}} {
		allowed, e := s.limiter.Allow(ctx, base64.RawURLEncoding.EncodeToString(s.digest("limit_"+scope.name, []byte(scope.value))))
		if e != nil {
			return out, command.ErrUnavailable
		}
		if !allowed {
			return out, ErrRateLimited
		}
	}
	codeHash := s.digest("code", []byte(code))
	requestHash := s.digest("client_request", []byte(clientID))
	nonce := make([]byte, 32)
	defer clear(nonce)
	if _, e := rand.Read(nonce); e != nil {
		return out, command.ErrUnavailable
	}
	receiptID := uuid.NewString()
	claimed, e := claimExchange(ctx, s.db, receiptID, codeHash, requestHash, nonce, s.config.ExchangeTTL)
	if e != nil {
		return out, e
	}
	if !claimed {
		return s.replay(ctx, codeHash, requestHash)
	}
	// 一次性code在外部调用后可能已消费；失败或进程中断均不擅自重放交换。
	openid, e := s.exchange.Exchange(ctx, code)
	defer clear(openid)
	if e != nil {
		if errors.Is(e, command.ErrUnauthenticated) {
			return out, command.ErrUnauthenticated
		}
		return out, command.ErrUnavailable
	}
	if len(openid) == 0 {
		return out, command.ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(s.digest("session_token", nonce))
	hash := sha256.Sum256([]byte(token))
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := setTimeout(ctx, db, s.config.Command.LockTimeout); e != nil {
			return e
		}
		id, e := s.identity.ResolveOpenID(ctx, db, openid)
		if errors.Is(e, identitydata.ErrInactive) {
			return command.ErrUnauthenticated
		}
		if e != nil {
			return e
		}
		// 身份解析先取得全局身份锁和账号guard，随后才锁回执及会话，不逆序取得身份锁。
		receipt, e := exchangeForUpdate(ctx, db, codeHash)
		if e != nil {
			return e
		}
		var now time.Time
		if e = databaseNow(ctx, db, &now); e != nil {
			return e
		}
		if receipt.ID != receiptID || receipt.Status != "processing" || !receipt.ExpiresAt.After(now) {
			return command.ErrUnauthenticated
		}
		var epoch int64
		if e = accountEpoch(ctx, db, id, &epoch); e != nil {
			return e
		}
		sessionID := uuid.NewString()
		if e = insertSession(ctx, db, sessionID, id, hash[:], epoch, now.Add(s.config.SessionTTL)); e != nil {
			return e
		}
		if e = finishExchange(ctx, db, receiptID, sessionID); e != nil {
			return e
		}
		out.Me, e = s.project(ctx, db, id, now)
		return e
	})
	if err == nil {
		out.Token = token
	}
	return out, err
}

func (s *Service) replay(ctx context.Context, codeHash, requestHash []byte) (out Session, err error) {
	receipt, e := readExchange(ctx, s.db, codeHash)
	if e != nil {
		return out, e
	}
	if !hmac.Equal(receipt.RequestHash, requestHash) {
		return out, command.ErrIdempotencyConflict
	}
	if receipt.Status != "completed" || len(receipt.Nonce) != 32 {
		return out, command.ErrUnauthenticated
	}
	token := base64.RawURLEncoding.EncodeToString(s.digest("session_token", receipt.Nonce))
	defer clear(receipt.Nonce)
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := setTimeout(ctx, db, s.config.Command.LockTimeout); e != nil {
			return e
		}
		id, now, e := authorize(ctx, db, token)
		if e != nil {
			return e
		}
		if !receipt.ExpiresAt.After(now) {
			return command.ErrUnauthenticated
		}
		out.Me, e = s.project(ctx, db, id, now)
		return e
	})
	if err == nil {
		out.Token = token
	}
	return out, err
}

// Current 每次按数据库当前生命周期/代次/处罚重新投影，不从Redis读取授权。
func (s *Service) Current(ctx context.Context, token, requestID string) (out Me, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		if err != nil {
			out = Me{}
		}
		s.complete("current", requestID, start, err)
	}()
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := setTimeout(ctx, db, s.config.Command.LockTimeout); e != nil {
			return e
		}
		id, now, e := authorize(ctx, db, token)
		if e != nil {
			return e
		}
		out, e = s.project(ctx, db, id, now)
		return e
	})
	return out, err
}

// UpdateProfile 受罚会话仅提供受限资料，不能通过资料写入口获取普通业务权限。
func (s *Service) UpdateProfile(ctx context.Context, token, key, requestID string, input ProfileInput) (out Me, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		if err != nil {
			out = Me{}
		}
		s.complete("profile", requestID, start, err)
	}()
	if !validKey(key) || strings.TrimSpace(input.Nickname) == "" || input.HideIdentityTags == nil || input.ExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	definition := command.Definition{
		Kind: "student.profile", Method: "PUT", ResourcePath: "/api/v1/me/profile", ActorKind: "student", Access: command.Ordinary,
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(context.Context, *command.Tx) error { return nil },
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			guard, ok := tx.Guard(tx.Actor().ID)
			if !ok {
				return "", command.ErrInternal
			}
			if guard.Version != input.ExpectedVersion {
				return "", command.ErrVersionConflict
			}
			return guard.ID, replaceProfile(ctx, tx.DB(), guard.ID, input)
		},
		Resolve: func(ctx context.Context, tx *command.Tx, id string) error {
			var e error
			out, e = s.project(ctx, tx.DB(), id, tx.Now())
			return e
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: requestID, Request: input})
	return out, err
}

// Logout 允许受罚主体撤销当前有效会话；撤销后的旧令牌不能当作有效身份重放。
func (s *Service) Logout(ctx context.Context, token, key, requestID string) (err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		s.complete("logout", requestID, start, err)
	}()
	if !validKey(key) {
		return command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := setTimeout(ctx, db, s.config.Command.LockTimeout); e != nil {
			return e
		}
		_, now, e := authorize(ctx, db, token)
		if e != nil {
			return e
		}
		hash := sha256.Sum256([]byte(token))
		return revoke(ctx, db, hash[:], now)
	})
}

// project 仅提示显式装配且当前资格允许的动作，不把静态协议目录当成实际授权。
func (s *Service) project(ctx context.Context, db *gorm.DB, id string, at time.Time) (Me, error) {
	out, e := projection(ctx, db, id, at)
	if e == nil && s.config.DeletionRequests {
		out.AllowedActions = append(out.AllowedActions, "postMeDeletionRequests")
	}
	if e == nil && s.config.StudentCardAssets && slices.Contains(out.AllowedActions, "putMeProfile") {
		out.AllowedActions = append(out.AllowedActions, "postMeStudentCardAssets")
	}
	if e == nil && s.config.VerificationRequests && slices.Contains(out.AllowedActions, "putMeProfile") && (out.Verification == "unverified" || out.Verification == "rejected") {
		out.AllowedActions = append(out.AllowedActions, "postMeVerificationRequests")
	}
	if e == nil && s.config.Wishes && out.Verification == "verified" && slices.Contains(out.AllowedActions, "putMeProfile") {
		out.AllowedActions = append(out.AllowedActions, "postWishes")
	}
	if e == nil && s.config.Safety {
		out.AllowedActions = append(out.AllowedActions, "postMeAppeals", "getMeAppeals", "getMeWarnings", "postMeWarningsIdDisplay", "postMeWarningsIdAck")
		if slices.Contains(out.AllowedActions, "putMeProfile") {
			// 解除提示归属当前Me，具体目标和版本仍取本人黑名单条目，不发送历史别名。
			enabled, failure := blockpolicy.Enabled(ctx, db)
			if failure != nil {
				return Me{}, failure
			}
			if enabled {
				out.AllowedActions = append(out.AllowedActions, "interactionBlockCreate", "interactionBlockDelete", "interactionBlocksList")
			} else {
				out.AllowedActions = append(out.AllowedActions, "postMeBlocks", "deleteMeBlocksTarget_Id")
			}
			out.AllowedActions = append(out.AllowedActions, "postReports", "getReportTypes")
		}
	}
	return out, e
}
