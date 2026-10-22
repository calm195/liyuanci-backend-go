// Package verification 提供加密人工认证申请和当前角色授权的后台审核。
package verification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/identity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// Rules 验证学校权威姓名和学号规则，不得记录输入；不在服务内臆造学号归一化。
// 无效材料返回command.ErrInvalid，供应商故障返回其他错误并故障关闭。
type Rules interface {
	Validate(context.Context, string, string) error
}

// Binder 必须在已有持有身份全局锁的事务中完成双代学号唯一占用。
type Binder interface {
	BindStudentNumber(context.Context, *gorm.DB, string, []byte) error
	ReserveStudentNumber(context.Context, *gorm.DB, string, []byte) error
}

// Objects 读取私有对象的精确不可变版本，返回体由服务关闭；不得提供公开URL或记录内容。
type Objects interface {
	Open(context.Context, string, string) (io.ReadCloser, error)
}

// Config 要求显式注入独立回执密钥及图片内存上限，不隐含生产配置。
type Config struct {
	Command      command.Config
	ReceiptKey   []byte
	MaxCardBytes int64
}

// Input 是待审材料；首次省略版本，驳回重提必须使用申请版本。
type Input struct {
	StudentNumber   string `json:"student_number"`
	Name            string `json:"name"`
	AssetID         string `json:"asset_id"`
	ExpectedVersion *int64 `json:"expected_version,omitempty"`
}

// Decision 是后台的期望版本审核意见。
type Decision struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason,omitempty"`
	RevisionSuggestion string `json:"revision_suggestion,omitempty"`
	ExpectedVersion    int64  `json:"expected_version"`
}

// Result 是本人或处理后台可读的最小申请状态。
type Result struct {
	RequestID          string  `json:"request_id"`
	Version            int64   `json:"version"`
	Status             string  `json:"status"`
	Reason             *string `json:"reason,omitempty"`
	RevisionSuggestion *string `json:"revision_suggestion,omitempty"`
}

// Case 是通过受控详情访问才能获得的完整审核材料。
type Case struct {
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	Status        string `json:"status"`
	Name          string `json:"name"`
	StudentNumber string `json:"student_number"`
	AssetID       string `json:"asset_id"`
}

// Summary 不包含姓名、学号、账号或学生卡引用。
type Summary struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Status  string `json:"status"`
	Kind    string `json:"kind"`
}

// Page 使用随机UUID的稳定升序游标分页，限制每页材料数。
type Page struct {
	Items      []Summary `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}
type material struct {
	Name          string `json:"name"`
	StudentNumber string `json:"student_number"`
}

// Service 不拥有依赖生命周期；学生和后台应分别使用最小权限连接池构造。
type Service struct {
	db      *gorm.DB
	engine  *command.Engine
	logger  *zap.Logger
	codec   *securedata.Codec
	binder  Binder
	rules   Rules
	objects Objects
	cfg     Config
}

// New 仅构造注入模块，不注册路由或执行迁移。
func New(db *gorm.DB, log *zap.Logger, codec *securedata.Codec, binder Binder, rules Rules, objects Objects, cfg Config) (*Service, error) {
	if log == nil || codec == nil || binder == nil || rules == nil || objects == nil || len(cfg.ReceiptKey) < 32 || cfg.MaxCardBytes < 1 || cfg.MaxCardBytes > 32<<20 {
		return nil, command.ErrInvalid
	}
	engine, e := command.New(db, log, cfg.Command)
	if e != nil {
		return nil, e
	}
	cfg.ReceiptKey = append([]byte(nil), cfg.ReceiptKey...)
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), engine: engine, logger: log, codec: codec, binder: binder, rules: rules, objects: objects, cfg: cfg}, nil
}

// ErrAssetNotReady 表示本人学生卡尚未通过扫描，不能提交或审批通过。
var ErrAssetNotReady = command.ErrAssetNotReady

func validKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}

func validID(id string) bool { v, e := uuid.Parse(id); return e == nil && v.String() == id }
func safe(e error) error {
	if e == nil {
		return nil
	}
	for _, known := range []error{command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrConflict, command.ErrInternal, ErrAssetNotReady} {
		if errors.Is(e, known) {
			return known
		}
	}
	if errors.Is(e, identity.ErrConflict) {
		return command.ErrConflict
	}
	return command.ErrUnavailable
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
	if len(rid) > 128 || strings.ContainsFunc(rid, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) {
		rid = ""
	}
	s.logger.Info("人工认证操作完成", zap.String("operation", op), zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
func (s *Service) digest(v any) string {
	b, _ := json.Marshal(v)
	defer clear(b)
	m := hmac.New(sha256.New, s.cfg.ReceiptKey)
	m.Write([]byte("identity_review\x00"))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}
func projection(r entity.IdentityReview) Result {
	return Result{r.ID, r.Version, r.Status, r.Reason, r.RevisionSuggestion}
}
func (s *Service) decrypt(ctx context.Context, r entity.IdentityReview) (material, error) {
	var env securedata.Envelope
	var m material
	if json.Unmarshal(r.DataCipher, &env) != nil {
		return m, command.ErrUnavailable
	}
	b, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "identity_review", ObjectID: r.ID, Version: uint64(r.Version)}, env)
	if e != nil {
		return m, e
	}
	defer clear(b)
	e = json.Unmarshal(b, &m)
	return m, e
}

// Submit 原子提交待审材料、状态和回执；待审图片必须已通过扫描且属于当前账号。
func (s *Service) Submit(ctx context.Context, token, key, rid string, in Input) (out Result, err error) {
	defer func() {
		if err != nil {
			out = Result{}
		}
	}()
	defer s.done("submit", rid, time.Now(), &err)
	if !validKey(key) || !validID(in.AssetID) || !utf8.ValidString(in.Name) || !utf8.ValidString(in.StudentNumber) || len(in.Name) > 256 || len(in.StudentNumber) > 128 || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.StudentNumber) == "" || in.ExpectedVersion != nil && *in.ExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	// 先验证当前会话，再调用外部学校规则；写事务仍重新检查会话和处罚。
	if e := s.transaction(ctx, func(db *gorm.DB) error {
		_, e := s.authorize(db.Statement.Context, db, token, "student", command.Discovery{})
		return e
	}); e != nil {
		return out, e
	}
	if e := s.rules.Validate(ctx, in.StudentNumber, in.Name); e != nil {
		return out, safe(e)
	}
	id := uuid.NewString()
	d := command.Definition{Kind: "identity_review.submit", Method: "POST", ResourcePath: "/api/v1/me/verification-requests", ActorKind: "student", Access: command.Ordinary, IdentityLock: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, sub command.Subject) (command.Discovery, error) {
		plan := command.Discovery{Resources: []command.Resource{{Kind: command.Asset, ID: in.AssetID}}}
		r, e := latest(db.WithContext(ctx), sub.ID)
		if e == nil {
			plan.Resources = append(plan.Resources, command.Resource{Kind: command.IdentityReview, ID: r.ID})
		} else if !errors.Is(e, command.ErrNotFound) {
			return plan, e
		}
		return plan, nil
	}
	// 重放仍复核账号归属；首次业务状态检查放在Apply，避免成功回执被新状态误判。
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		_, e := asset(tx.DB().WithContext(ctx), in.AssetID, tx.Actor().ID)
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		actor := tx.Actor().ID
		guard, _ := tx.Guard(actor)
		if guard.Verification == "verified" {
			return "", command.ErrConflict
		}
		r, e := latest(db, actor)
		version := int64(1)
		if e == nil {
			if in.ExpectedVersion == nil {
				return "", command.ErrInvalid
			}
			if r.Version != *in.ExpectedVersion {
				return "", command.ErrVersionConflict
			}
			if r.Status != "rejected" {
				return "", command.ErrConflict
			}
			id = r.ID
			version = r.Version + 1
		} else if !errors.Is(e, command.ErrNotFound) {
			return "", e
		} else if in.ExpectedVersion != nil {
			return "", command.ErrVersionConflict
		}
		// 注册即占用学号；驳回材料更正与释放旧占用在本事务原子完成。
		if e := s.binder.ReserveStudentNumber(ctx, db, actor, []byte(in.StudentNumber)); e != nil {
			return "", safe(e)
		}
		b, e := json.Marshal(material{Name: in.Name, StudentNumber: in.StudentNumber})
		if e != nil {
			return "", e
		}
		defer clear(b)
		env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "identity_review", ObjectID: id, Version: uint64(version)}, b)
		if e != nil {
			return "", e
		}
		encoded, e := json.Marshal(env)
		if e != nil {
			return "", e
		}
		row := entity.IdentityReview{ID: id, AccountID: actor, Version: version, AssetID: in.AssetID, Status: "pending", DataCipher: encoded}
		if version == 1 {
			e = createReview(db, &row)
		} else {
			e = updateReview(db, id, map[string]any{"version": version, "asset_id": in.AssetID, "status": "pending", "data_cipher": json.RawMessage(encoded), "reason": nil, "revision_suggestion": nil, "reviewer_id": nil, "reviewed_at": nil})
		}
		if e != nil {
			return "", e
		}
		if e = setVerification(db, actor, "pending"); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, id string) error {
		r, e := review(tx.DB().WithContext(ctx), id)
		if e != nil {
			return e
		}
		if r.AccountID != tx.Actor().ID {
			return command.ErrNotFound
		}
		out = projection(r)
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in)})
	if err != nil {
		out = Result{}
	}
	return
}

// Decide 持当前审核角色及主体锁完成单次决定，通过时原子占用学号并写入加密姓名。
func (s *Service) Decide(ctx context.Context, token, key, rid, id string, in Decision) (out Result, err error) {
	defer func() {
		if err != nil {
			out = Result{}
		}
	}()
	defer s.done("decision", rid, time.Now(), &err)
	if !validKey(key) || !validID(id) || in.ExpectedVersion < 1 || (in.Decision != "approved" && in.Decision != "rejected") || len(in.Reason) > 2000 || len(in.RevisionSuggestion) > 2000 || !utf8.ValidString(in.Reason) || !utf8.ValidString(in.RevisionSuggestion) || in.Decision == "rejected" && (strings.TrimSpace(in.Reason) == "" || strings.TrimSpace(in.RevisionSuggestion) == "") {
		return out, command.ErrInvalid
	}
	if e := s.transaction(ctx, func(db *gorm.DB) error {
		_, e := s.authorize(db.Statement.Context, db, token, "admin", command.Discovery{})
		return e
	}); e != nil {
		return out, e
	}
	d := command.Definition{Kind: "identity_review.decision", Method: "POST", ResourcePath: "/api/v1/admin/identity-reviews/" + id + "/decision", ActorKind: "admin", AllowedRoles: []string{"review_operator", "super_admin"}, IdentityLock: true, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		r, e := review(db.WithContext(ctx), id)
		if e != nil {
			return command.Discovery{}, e
		}
		return command.Discovery{Accounts: []string{r.AccountID}, Resources: []command.Resource{{Kind: command.Asset, ID: r.AssetID}, {Kind: command.IdentityReview, ID: r.ID}}}, nil
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		r, e := review(tx.DB().WithContext(ctx), id)
		if e != nil {
			return e
		}
		g, ok := tx.Guard(r.AccountID)
		if !ok || g.Lifecycle != "active" {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		r, e := review(db, id)
		if e != nil {
			return "", e
		}
		if r.Status != "pending" || len(r.DataCipher) == 0 {
			return "", command.ErrConflict
		}
		if _, e = asset(db, r.AssetID, r.AccountID); e != nil {
			return "", e
		}
		verification := "rejected"
		if in.Decision == "approved" {
			m, e := s.decrypt(ctx, r)
			if e != nil {
				return "", e
			}
			if e = s.binder.BindStudentNumber(ctx, db, r.AccountID, []byte(m.StudentNumber)); e != nil {
				return "", safe(e)
			}
			env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "name", ObjectID: r.AccountID, Version: 1}, []byte(m.Name))
			if e != nil {
				return "", e
			}
			b, e := json.Marshal(env)
			if e != nil {
				return "", e
			}
			if e = createName(db, r.AccountID, b); e != nil {
				return "", e
			}
			verification = "verified"
		}
		actor := tx.Actor().ID
		updates := map[string]any{"status": in.Decision, "version": r.Version + 1, "reviewer_id": actor, "reviewed_at": tx.Now(), "reason": nil, "revision_suggestion": nil}
		// 密文AAD绑定原版本，状态版本递增时同步重封装，详情不得用新版本解密旧密文。
		m, e := s.decrypt(ctx, r)
		if e != nil {
			return "", e
		}
		b, e := json.Marshal(m)
		if e != nil {
			return "", e
		}
		defer clear(b)
		env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "identity_review", ObjectID: id, Version: uint64(r.Version + 1)}, b)
		if e != nil {
			return "", e
		}
		encoded, e := json.Marshal(env)
		if e != nil {
			return "", e
		}
		updates["data_cipher"] = json.RawMessage(encoded)
		if in.Decision == "rejected" {
			updates["reason"] = in.Reason
			updates["revision_suggestion"] = in.RevisionSuggestion
		}
		if e = updateReview(db, id, updates); e != nil {
			return "", e
		}
		if e = setVerification(db, r.AccountID, verification); e != nil {
			return "", e
		}
		if e = audit(db, actor, r.AccountID, "identity_review.decision", "allowed"); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, id string) error {
		r, e := review(tx.DB().WithContext(ctx), id)
		if e == nil {
			out = projection(r)
		}
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.IdentityReview, ID: id}, Value: in.ExpectedVersion}}})
	if err != nil {
		out = Result{}
	}
	return
}
