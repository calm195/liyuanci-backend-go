// Package privacy 提供独立删除意图、即时隐私屏障及可重试普通数据清理。
package privacy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// Config 要求独立回执密钥、清理预算及有限匿名回执保留期。
type Config struct {
	// Redactor 缺失时涉及幸存内容的清理保持processing，禁止直接保留可能指认注销人的旧原稿。
	Redactor   Redactor
	Command    command.Config
	Key        []byte
	ReceiptTTL time.Duration
	RetryDelay time.Duration
}

// Eraser 必须幂等且响应context，覆盖目标在线对象全部版本、回收站及应用缓存。
// 实现须先持久禁止目标及所列对象的迟到写入，再等待在途写入退出并清除全部版本；不得用单次Delete冒充已建立写屏障。
// 调用发生于数据库事务之外；未确认或未配置时不得报告completed。备份按独立日志恢复重删。
// 输入只含随机标识及加密对象元数据，适配器不得输出正文或身份到日志。
type Eraser interface {
	Erase(context.Context, Intent, []Object) error
}

// Object 是只供可信清理适配器解密的对象定位，不向HTTP返回。
type Object struct {
	// ReferenceOnly 只表示应用缓存/普通引用定位，不是可解密对象存储键；适配器必须抑制迟到写入并确认清除。
	ReferenceOnly bool
	ID            string
	KeyCipher     json.RawMessage
	Version       string
	Evidence      bool
}

// Service 不拥有数据库、日志、密钥或外部清理适配器；不同权限池分别构造账号和证据实例。
type Service struct {
	db      *gorm.DB
	engine  *command.Engine
	journal Journal
	codec   *securedata.Codec
	eraser  Eraser
	logger  *zap.Logger
	cfg     Config
}

// DeriveKey 为删除回执及命令摘要派生独立用途密钥。
func DeriveKey(root []byte) []byte {
	h := hmac.New(sha256.New, root)
	h.Write([]byte("privacy.receipt.v1"))
	return h.Sum(nil)
}

// New 显式构造服务，缺失独立日志拒绝启用，缺失清理适配器允许受理但保持processing。
func New(db *gorm.DB, journal Journal, codec *securedata.Codec, eraser Eraser, logger *zap.Logger, cfg Config) (*Service, error) {
	if journal == nil || codec == nil || logger == nil || len(cfg.Key) < 32 || cfg.ReceiptTTL < time.Minute || cfg.ReceiptTTL > 90*24*time.Hour || cfg.RetryDelay < time.Millisecond {
		return nil, command.ErrInvalid
	}
	engine, e := command.New(db, logger, cfg.Command)
	if e != nil {
		return nil, e
	}
	cfg.Key = append([]byte(nil), cfg.Key...)
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), engine: engine, journal: journal, codec: codec, eraser: eraser, logger: logger, cfg: cfg}, nil
}

// DeletionInput 绑定本人确认及账号版本，不接受客户端主体。
type DeletionInput struct {
	Confirmation    string `json:"confirmation"`
	ExpectedVersion int64  `json:"expected_version"`
}

// EvidenceInput 绑定证据范围版本与删除理由，只有当前超管可执行。
type EvidenceInput struct {
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expected_version"`
}

// Accepted 仅表示注销生效，不表示物理清理完成。
type Accepted struct {
	Receipt string `json:"deletion_receipt"`
	Status  string `json:"status"`
}

// Receipt 仅含状态及安全重试提示，无账号、正文、对象或案件引用。
type Receipt struct {
	Status         string `json:"status"`
	RetryableError string `json:"retryable_error,omitempty"`
}

// EvidenceAccepted 是证据删除持久待办，不声称即时已删完。
type EvidenceAccepted struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Status  string `json:"status"`
}

func validID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && u != uuid.Nil && u.String() == id
}
func validKey(k string) bool { return len(k) > 0 && len(k) <= 128 && !strings.ContainsAny(k, "\r\n") }
func (s *Service) mac(purpose string, v any) []byte {
	b, _ := json.Marshal(v)
	defer clear(b)
	h := hmac.New(sha256.New, s.cfg.Key)
	h.Write([]byte(purpose))
	h.Write(b)
	return h.Sum(nil)
}
func (s *Service) jobID(scope, target, key string) string {
	b := s.mac("deletion.job", []string{scope, target, key})
	u, _ := uuid.FromBytes(b[:16])
	return u.String()
}
func (s *Service) receipt(id string) string {
	return id + "." + base64.RawURLEncoding.EncodeToString(s.mac("deletion.receipt", id))
}
func (s *Service) done(op, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	if *err != nil {
		known := false
		for _, e := range []error{command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrConflict, command.ErrRetry, command.ErrInternal} {
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
	s.logger.Info("隐私删除操作完成", fields...)
}
func (s *Service) createJob(db *gorm.DB, in Intent) error {
	var subject *string
	if in.Scope == "account" {
		subject = &in.Target
	}
	return db.Create(&entity.DeletionJob{SourceModule: in.SourceModule, JobID: in.ID, SubjectRef: subject, Scope: in.Scope, TargetRef: &in.Target, Phase: "blocked", ExpiresAt: time.Now().Add(s.cfg.ReceiptTTL)}).Error
}

// Request 先持久独立意图，再同事务使注销、关系矩阵及公开屏障生效；返回后会话已撤销。
func (s *Service) Request(ctx context.Context, token, key, rid string, in DeletionInput) (out Accepted, err error) {
	defer func() {
		if err != nil {
			out = Accepted{}
		}
	}()
	defer s.done("account.request", rid, time.Now(), &err)
	if in.Confirmation != "delete_account" || in.ExpectedVersion < 1 || !validKey(key) {
		return out, command.ErrInvalid
	}
	var intent Intent
	d := command.Definition{Kind: "privacy.delete_account", Method: "POST", ResourcePath: "/api/v1/me/deletion-requests", ActorKind: "student", Access: command.Restricted, IdentityLock: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, actor command.Subject) (command.Discovery, error) {
		return accountPlan(ctx, db, actor.ID)
	}
	d.Authorize = func(_ context.Context, tx *command.Tx) error {
		g, ok := tx.Guard(tx.Actor().ID)
		if !ok {
			return command.ErrUnauthenticated
		}
		if g.Version != in.ExpectedVersion {
			return command.ErrVersionConflict
		}
		intent = Intent{ID: s.jobID("account", tx.Actor().ID, key), Scope: "account", Target: tx.Actor().ID}
		return nil
	}
	d.PersistIntent = func(ctx context.Context, _ *command.Tx) error { return s.journal.Append(ctx, intent) }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		if e := activateAccount(ctx, tx, intent.Target); e != nil {
			return "", e
		}
		return intent.ID, s.createJob(tx.DB().WithContext(ctx), intent)
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, id string) error {
		var job entity.DeletionJob
		if e := tx.DB().WithContext(ctx).Where("job_id=? AND scope='account' AND target_ref=?", id, tx.Actor().ID).Take(&job).Error; e != nil {
			return e
		}
		out = Accepted{Receipt: s.receipt(id), Status: "processing"}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: in})
	return
}

// Status 只接受带用途认证的高熵回执；已过期与伪造均404，不返回可枚举UUID状态。
func (s *Service) Status(ctx context.Context, rid, handle string) (out Receipt, err error) {
	defer func() {
		if err != nil {
			out = Receipt{}
		}
	}()
	defer s.done("receipt.read", rid, time.Now(), &err)
	id, tag, ok := strings.Cut(handle, ".")
	if !ok || !validID(id) {
		return out, command.ErrNotFound
	}
	b, e := base64.RawURLEncoding.DecodeString(tag)
	if e != nil || !hmac.Equal(b, s.mac("deletion.receipt", id)) {
		return out, command.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	var rows []entity.DeletionJob
	if e = s.db.WithContext(ctx).Where("job_id=? AND scope='account' AND expires_at>clock_timestamp()", id).Find(&rows).Error; e != nil {
		return out, e
	}
	if len(rows) != 1 {
		return out, command.ErrNotFound
	}
	out.Status = "processing"
	if rows[0].Phase == "completed" {
		out.Status = "completed"
	} else if rows[0].ErrorCode != nil {
		out.RetryableError = *rows[0].ErrorCode
	}
	return
}

// DeleteEvidence 只删除明确证据范围，撤销角色不能重放；理由保留在待删证据域并随该范围清除。
func (s *Service) DeleteEvidence(ctx context.Context, token, key, rid, id string, in EvidenceInput) (EvidenceAccepted, error) {
	return s.deleteEvidence(ctx, token, key, rid, id, in, false)
}

// DeleteWhisperEvidence 只受理树洞危机证据，使用独立命名空间和生命周期回执。
func (s *Service) DeleteWhisperEvidence(ctx context.Context, token, key, rid, id string, in EvidenceInput) (EvidenceAccepted, error) {
	return s.deleteEvidence(ctx, token, key, rid, id, in, true)
}

func (s *Service) deleteEvidence(ctx context.Context, token, key, rid, id string, in EvidenceInput, whispers bool) (out EvidenceAccepted, err error) {
	defer func() {
		if err != nil {
			out = EvidenceAccepted{}
		}
	}()
	defer s.done("evidence.request", rid, time.Now(), &err)
	if !validID(id) || !validKey(key) || in.ExpectedVersion < 1 || !utf8.ValidString(in.Reason) || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2000 {
		return out, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	// 权限预检先于证据发现，普通运营不得从404/版本差异探测案件。
	if err = s.superPreflight(ctx, token); err != nil {
		return
	}
	var ec entity.EvidenceCase
	if e := s.db.WithContext(ctx).Where("id=? OR id IN (SELECT case_id FROM evidence.evidence_items WHERE id=?)", id, id).Take(&ec).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return out, command.ErrNotFound
		}
		return out, e
	}

	var tree int64
	if e := s.db.WithContext(ctx).Model(&entity.EvidenceCrisisCase{}).Where("evidence_case_id=? AND content_type IN ('whisper_post','whisper_comment','whisper_nickname','whisper_capsule')", ec.ID).Count(&tree).Error; e != nil {
		return out, command.ErrUnavailable
	}
	if whispers != (tree > 0) {
		return out, command.ErrNotFound
	}
	id = ec.ID
	var intent Intent
	target := command.Resource{Kind: command.EvidenceCase, ID: id}
	d := command.Definition{Kind: "privacy.delete_evidence", Method: "DELETE", ResourcePath: "/api/v1/admin/evidence/" + id, ActorKind: "admin", AllowedRoles: []string{"super_admin"}, RequiredExpectations: []string{"expected_version"}}
	if whispers {
		d.Kind = "privacy.delete_whisper_evidence"
		d.Method = "POST"
		d.ResourcePath = "/api/v1/admin/whispers/evidence/" + id + "/deletion-requests"
		d.RetainReceipt = true
	}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return evidencePlan(ctx, db, id)
	}
	d.Authorize = func(_ context.Context, tx *command.Tx) error {
		intent = Intent{ID: s.jobID("evidence", id, key), Scope: "evidence", Target: id}
		if whispers {
			intent.SourceModule = "whispers"
		}
		return nil
	}
	d.PersistIntent = func(ctx context.Context, tx *command.Tx) error {
		var current entity.EvidenceCase
		if e := tx.DB().WithContext(ctx).Where("id=?", id).Take(&current).Error; e != nil {
			return e
		}
		if current.Status == "deletion_pending" {
			return command.ErrConflict
		}
		return s.journal.Append(ctx, intent)
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		if e := activateEvidence(ctx, db, id); e != nil {
			return "", e
		}
		aid := uuid.NewString()
		b, _ := json.Marshal(in.Reason)
		defer clear(b)
		env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "crisis_reason", ObjectID: aid, Version: 1}, b)
		if e != nil {
			return "", e
		}
		raw, _ := json.Marshal(env)
		if e = db.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&entity.EvidenceAction{ID: aid, CaseID: id, Actor: tx.Actor().ID, Action: "delete_requested", ReasonCipher: raw, At: tx.Now()}).Error; e != nil {
			return "", e
		}
		return intent.ID, s.createJob(db, intent)
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var job entity.DeletionJob
		if e := tx.DB().WithContext(ctx).Where("job_id=? AND scope='evidence'", ref).Take(&job).Error; e != nil {
			return e
		}
		out = EvidenceAccepted{ID: ref, Version: 1, Status: "pending"}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: struct {
		Reason  []byte
		Version int64
	}{s.mac("evidence.reason", in.Reason), in.ExpectedVersion}, Expectations: []command.Expectation{{Name: "expected_version", Target: target, Value: in.ExpectedVersion}}})
	return
}

func (s *Service) superPreflight(ctx context.Context, token string) error {
	if token == "" {
		return command.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var rows []struct {
		ID      string
		Allowed bool
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	e := s.db.WithContext(ctx).Raw(`SELECT a.id,EXISTS(SELECT 1 FROM public.admin_role_bindings b WHERE b.admin_id=a.id AND b.role='super_admin') AS allowed FROM public.sessions s JOIN public.admin_accounts a ON a.id=s.admin_id WHERE s.token_hash=? AND s.subject_kind='admin' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND s.auth_epoch=a.auth_epoch AND a.status='active'`, hash[:]).Scan(&rows).Error
	if e != nil {
		return e
	}
	if len(rows) != 1 {
		return command.ErrUnauthenticated
	}
	if !rows[0].Allowed {
		return command.ErrForbidden
	}
	return nil
}

// EvidenceStatus 返回超管当前可读的最小清理进度；不包含证据目标、理由或原始身份。
func (s *Service) EvidenceStatus(ctx context.Context, token, rid, id string) (Receipt, error) {
	return s.evidenceStatus(ctx, token, rid, id, "legacy")
}

// WhisperEvidenceStatus 只返回树洞删除待办，完成后也不跨命名空间提供回执。
func (s *Service) WhisperEvidenceStatus(ctx context.Context, token, rid, id string) (Receipt, error) {
	return s.evidenceStatus(ctx, token, rid, id, "whispers")
}

func (s *Service) evidenceStatus(ctx context.Context, token, rid, id, module string) (out Receipt, err error) {
	defer func() {
		if err != nil {
			out = Receipt{}
		}
	}()
	defer s.done("evidence.progress", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	if err = s.superPreflight(ctx, token); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	var row entity.DeletionJob
	if e := s.db.WithContext(ctx).Where("job_id=? AND scope='evidence' AND source_module=? AND expires_at>clock_timestamp()", id, module).Take(&row).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return out, command.ErrNotFound
		}
		return out, e
	}
	out.Status = "processing"
	if row.Phase == "completed" {
		out.Status = "completed"
	} else if row.ErrorCode != nil {
		out.RetryableError = *row.ErrorCode
	}
	return
}
