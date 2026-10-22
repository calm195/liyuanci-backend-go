// Package identity 提供身份字段加密存储及双代检索占用，不代表学生资格审核通过。
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// ErrConflict 表示身份已被占用或账号已绑定其他值，不包含可反查的身份信息。
var ErrConflict = errors.New("身份绑定冲突")

// ErrInactive 表示身份已映射到不可登录的生命周期，不能创建替代账号绕过注销屏障。
var ErrInactive = errors.New("身份账号不可登录")

// ErrUnavailable 表示密钥、存储或身份状态不可用，调用方不能降级明文或绕过查重。
var ErrUnavailable = errors.New("身份基础服务不可用")

// DigestService 使用外部独立检索密钥执行用途隔离 HMAC-SHA256；返回值必须为32字节。
// 实现须验证授权引用、响应取消，不得记录或保留身份原文，不得与数据包裹密钥复用。
type DigestService interface {
	Digest(context.Context, string, string, []byte) ([]byte, error)
}

// Store 持有显式注入的依赖，数据库会话关闭 SQL 日志以避免摘要或密文被输出。
type Store struct {
	db      *gorm.DB
	codec   *securedata.Codec
	digests DigestService
	logger  *zap.Logger
}

// New 构造身份存储，调用方仍负责连接池生命周期及命令授权。
func New(db *gorm.DB, codec *securedata.Codec, digests DigestService, logger *zap.Logger) (*Store, error) {
	if db == nil || codec == nil || digests == nil || logger == nil {
		return nil, ErrUnavailable
	}
	return &Store{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), codec: codec, digests: digests, logger: logger}, nil
}

type keyState struct {
	CurrentRef  string
	PreviousRef *string
}

func validPurpose(purpose string) bool { return purpose == "student_number" || purpose == "openid" }

func (s *Store) transaction(ctx context.Context, operation, purpose string, action func(*gorm.DB) error) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		// 不输出账号、摘要、引用或供应商错误原文，防止身份关联及凭据泄漏。
		s.logger.Info("身份存储操作完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if !validPurpose(purpose) {
		return ErrUnavailable
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 所有绑定和检索轮换共用数据库锁，避免配置切换和双代回填之间出现唯一性窗口。
		// 这是身份占用的受控串行入口，后续领域命令锁序必须先取得此锁再锁账号。
		if tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109174)").Error != nil {
			return ErrUnavailable
		}
		return action(tx)
	})
	if err != nil && !errors.Is(err, ErrConflict) {
		return ErrUnavailable
	}
	return err
}

// state 只读取检索状态投影，空记录仍由领域边界映射为不可用。
func state(tx *gorm.DB, purpose string) (keyState, error) {
	var result []keyState
	if tx.Model(&entity.IdentityLookupKey{}).Select("current_ref, previous_ref").Where("purpose = ?", purpose).Find(&result).Error != nil || len(result) != 1 {
		return keyState{}, ErrUnavailable
	}
	return result[0], nil
}

func (s *Store) digest(ctx context.Context, ref, purpose string, value []byte) ([]byte, error) {
	got, err := s.digests.Digest(ctx, ref, purpose, value)
	if err != nil || len(got) != 32 {
		return nil, ErrUnavailable
	}
	return got, nil
}

// Configure 显式初始化用途的检索代次；存在的配置仅允许同引用幂等重放。
func (s *Store) Configure(ctx context.Context, purpose, ref string) error {
	return s.transaction(ctx, "configure", purpose, func(tx *gorm.DB) error {
		if ref == "" {
			return ErrUnavailable
		}
		if _, err := s.digest(ctx, ref, purpose, []byte("key-availability-check")); err != nil {
			return err
		}
		if tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "purpose"}}, DoNothing: true}).Create(&entity.IdentityLookupKey{Purpose: purpose, CurrentRef: ref}).Error != nil {
			return ErrUnavailable
		}
		current, err := state(tx, purpose)
		if err != nil {
			return err
		}
		if current.CurrentRef != ref || current.PreviousRef != nil {
			return ErrConflict
		}
		return nil
	})
}

// Bind 为正常账号绑定规范身份字节；同账号同值可重放，改号或重新绑定须由后续审核命令处理。
// 本层不自行折叠大小写、去空格或改变学校学号规则，接入方须提供经权威规则验证的值。
func (s *Store) Bind(ctx context.Context, accountID, purpose string, value []byte) error {
	return s.transaction(ctx, "bind", purpose, func(tx *gorm.DB) error {
		return s.bind(ctx, tx, accountID, purpose, value)
	})
}

// BeginRotation 切换到受控双代状态；未完成的轮换禁止叠加第三代。
func (s *Store) BeginRotation(ctx context.Context, purpose, nextRef string) error {
	return s.transaction(ctx, "rotation_begin", purpose, func(tx *gorm.DB) error {
		current, err := state(tx, purpose)
		if err != nil {
			return err
		}
		if nextRef == "" || nextRef == current.CurrentRef || current.PreviousRef != nil {
			return ErrConflict
		}
		if _, err = s.digest(ctx, nextRef, purpose, []byte("key-availability-check")); err != nil {
			return err
		}
		if tx.Model(&entity.IdentityLookupKey{}).Where("purpose = ?", purpose).Updates(map[string]any{"previous_ref": gorm.Expr("current_ref"), "current_ref": nextRef, "version": gorm.Expr("version + 1")}).Error != nil {
			return ErrUnavailable
		}
		return nil
	})
}

// Backfill 有界回填缺失的新代映射，验证旧代摘要与解密值一致后才新增占用。
// 批次失败整体回滚，旧代仍可查重；敏感明文只在受控内存中短暂存在。
func (s *Store) Backfill(ctx context.Context, purpose string, limit int) (count int, err error) {
	err = s.transaction(ctx, "rotation_backfill", purpose, func(tx *gorm.DB) error {
		if limit < 1 || limit > 100 {
			return ErrUnavailable
		}
		current, e := state(tx, purpose)
		if e != nil {
			return e
		}
		if current.PreviousRef == nil {
			return ErrConflict
		}
		var rows []struct {
			AccountID      string
			ContentVersion int64
			Envelope       []byte
		}
		if missingClaims(tx, purpose, current.CurrentRef).
			Select("s.account_id", "s.content_version", "s.envelope").Order("s.account_id").Limit(limit).
			Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "s"}}).Find(&rows).Error != nil {
			return ErrUnavailable
		}
		for _, row := range rows {
			if e = s.backfillOne(ctx, tx, purpose, current, row.AccountID, row.ContentVersion, row.Envelope); e != nil {
				return e
			}
		}
		count = len(rows)
		return nil
	})
	if err != nil {
		count = 0
	}
	return count, err
}

func (s *Store) backfillOne(ctx context.Context, tx *gorm.DB, purpose string, current keyState, id string, version int64, encoded []byte) error {
	var envelope securedata.Envelope
	if json.Unmarshal(encoded, &envelope) != nil {
		return ErrUnavailable
	}
	plain, err := s.codec.Decrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, envelope)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(plain)
	previous, err := s.digest(ctx, *current.PreviousRef, purpose, plain)
	if err != nil {
		return err
	}
	var matches int64
	if tx.Model(&entity.IdentityLookupClaim{}).Where("account_id = ? AND purpose = ? AND key_ref = ? AND digest = ?", id, purpose, *current.PreviousRef, previous).Count(&matches).Error != nil || matches != 1 {
		return ErrUnavailable
	}
	next, err := s.digest(ctx, current.CurrentRef, purpose, plain)
	if err != nil {
		return err
	}
	if tx.Create(&entity.IdentityLookupClaim{Purpose: purpose, KeyRef: current.CurrentRef, Digest: next, AccountID: id}).Error != nil {
		return ErrConflict
	}
	return nil
}

// FinishRotation 仅在全部密文均有新代占用时清除旧代，检查和切换与新绑定共用事务锁。
func (s *Store) FinishRotation(ctx context.Context, purpose string) error {
	return s.transaction(ctx, "rotation_finish", purpose, func(tx *gorm.DB) error {
		current, err := state(tx, purpose)
		if err != nil {
			return err
		}
		if current.PreviousRef == nil {
			return ErrConflict
		}
		var missing int64
		if missingClaims(tx, purpose, current.CurrentRef).Count(&missing).Error != nil {
			return ErrUnavailable
		}
		if missing != 0 {
			return ErrConflict
		}
		if tx.Where("purpose = ? AND key_ref = ?", purpose, *current.PreviousRef).Delete(&entity.IdentityLookupClaim{}).Error != nil {
			return ErrUnavailable
		}
		if tx.Model(&entity.IdentityLookupKey{}).Where("purpose = ?", purpose).Updates(map[string]any{"previous_ref": nil, "version": gorm.Expr("version + 1")}).Error != nil {
			return ErrUnavailable
		}
		return nil
	})
}

// Reencrypt 在维护权限下按批重加密指定用途的旧信封，不修改内容版本或唯一占用。
// 目标引用必须与外部密钥服务实际包裹结果一致，防止配置漂移造成无限重加密。
func (s *Store) Reencrypt(ctx context.Context, purpose, targetRef string, limit int) (count int, err error) {
	err = s.transaction(ctx, "reencrypt", purpose, func(tx *gorm.DB) error {
		if targetRef == "" || limit < 1 || limit > 100 {
			return ErrUnavailable
		}
		var rows []struct {
			AccountID      string
			ContentVersion int64
			Envelope       []byte
		}
		if tx.Model(&entity.IdentitySecret{}).Select("account_id", "content_version", "envelope").
			Where("purpose = ? AND (envelope->>'KeyRef') IS DISTINCT FROM ?", purpose, targetRef).
			Order("account_id").Limit(limit).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error != nil {
			return ErrUnavailable
		}
		for _, row := range rows {
			if e := s.reencryptOne(ctx, tx, purpose, targetRef, row.AccountID, row.ContentVersion, row.Envelope); e != nil {
				return e
			}
		}
		count = len(rows)
		return nil
	})
	if err != nil {
		count = 0
	}
	return count, err
}

func (s *Store) reencryptOne(ctx context.Context, tx *gorm.DB, purpose, targetRef, id string, version int64, encoded []byte) error {
	var previous securedata.Envelope
	if json.Unmarshal(encoded, &previous) != nil {
		return ErrUnavailable
	}
	binding := securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}
	plain, err := s.codec.Decrypt(ctx, binding, previous)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(plain)
	next, err := s.codec.Encrypt(ctx, binding, plain)
	if err != nil || next.KeyRef != targetRef {
		return ErrUnavailable
	}
	serialized, err := json.Marshal(next)
	if err != nil {
		return ErrUnavailable
	}
	if tx.Model(&entity.IdentitySecret{}).Where("account_id = ? AND purpose = ?", id, purpose).Updates(map[string]any{"envelope": json.RawMessage(serialized)}).Error != nil {
		return ErrUnavailable
	}
	return nil
}

// ResolveOpenID 在调用方根事务内解析或创建微信身份；调用方必须先取身份锁，再锁账号和会话。
// 返回的账号不因此获得学生资格；外层负责会话、回执与身份绑定一并提交。
func (s *Store) ResolveOpenID(ctx context.Context, tx *gorm.DB, value []byte) (id string, err error) {
	start := time.Now()
	defer func() {
		result := "pending_commit"
		if err != nil {
			result = "failure"
			id = ""
		}
		s.logger.Info("微信身份解析暂存完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if tx == nil || tx.Statement == nil || len(value) == 0 {
		return "", ErrUnavailable
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return "", ErrUnavailable
	}
	tx = tx.WithContext(ctx).Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	// 与绑定/轮换使用同一锁，避免新账号穿透双代唯一占用；锁不能由调用方提前反序取得。
	if tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109174)").Error != nil {
		return "", ErrUnavailable
	}
	current, e := state(tx, "openid")
	if e != nil {
		return "", e
	}
	refs := []string{current.CurrentRef}
	if current.PreviousRef != nil {
		refs = append(refs, *current.PreviousRef)
	}
	values := make([][]byte, len(refs))
	for i, ref := range refs {
		values[i], e = s.digest(ctx, ref, "openid", value)
		if e != nil {
			return "", e
		}
		var rows []struct{ AccountID string }
		if tx.Model(&entity.IdentityLookupClaim{}).Select("account_id").Where("purpose='openid' AND key_ref=? AND digest=?", ref, values[i]).Find(&rows).Error != nil {
			return "", ErrUnavailable
		}
		if len(rows) == 1 {
			if id != "" && id != rows[0].AccountID {
				return "", ErrConflict
			}
			id = rows[0].AccountID
		}
	}
	if id != "" {
		var accounts []entity.Account
		if tx.Select("lifecycle").Where("id = ?", id).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&accounts).Error != nil {
			return "", ErrUnavailable
		}
		if len(accounts) != 1 || accounts[0].Lifecycle != "active" {
			return "", ErrInactive
		}
		return id, nil
	}
	id = uuid.NewString()
	envelope, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "openid", ObjectID: id, Version: 1}, value)
	if e != nil {
		return "", ErrUnavailable
	}
	encoded, e := json.Marshal(envelope)
	if e != nil {
		return "", ErrUnavailable
	}
	if tx.Create(&entity.Account{ID: id, Nickname: "新用户"}).Error != nil {
		return "", ErrUnavailable
	}
	if tx.Create(&entity.IdentitySecret{AccountID: id, Purpose: "openid", ContentVersion: 1, Envelope: json.RawMessage(encoded)}).Error != nil {
		return "", ErrUnavailable
	}
	for i, ref := range refs {
		if tx.Create(&entity.IdentityLookupClaim{Purpose: "openid", KeyRef: ref, Digest: values[i], AccountID: id}).Error != nil {
			return "", ErrUnavailable
		}
	}
	return id, nil
}

// missingClaims 复用新代缺口条件，回填与结束轮换必须使用相同存在性判定。
// 子查询只比较随机主体与检索引用；调用方的身份锁保持绑定、回填和切换串行。
func missingClaims(tx *gorm.DB, purpose, ref string) *gorm.DB {
	claims := tx.Model(&entity.IdentityLookupClaim{}).Select("1").
		Where("account_id = s.account_id AND purpose = s.purpose AND key_ref = ?", ref)
	return tx.Model(&entity.IdentitySecret{}).Table("public.identity_secrets AS s").
		Where("s.purpose = ?", purpose).Where("NOT EXISTS (?)", claims)
}

// BindStudentNumber 在已有命令事务中绑定学号，调用方必须先取得身份全局锁再锁账号。
// 只允许正常账号首次占用或同值重放，不允许审核修改既有已绑定身份。
func (s *Store) BindStudentNumber(ctx context.Context, tx *gorm.DB, accountID string, value []byte) (err error) {
	start := time.Now()
	defer func() {
		result := "pending_commit"
		if err != nil {
			result = "failure"
		}
		s.logger.Info("身份事务绑定完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if e := bindingTransaction(ctx, tx); e != nil {
		return e
	}
	return s.bind(ctx, tx.WithContext(ctx), accountID, "student_number", value)
}
func (s *Store) bind(ctx context.Context, tx *gorm.DB, accountID, purpose string, value []byte) error {
	canonical, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, accountID)
	if !canonical || len(value) == 0 {
		return ErrUnavailable
	}
	current, err := state(tx, purpose)
	if err != nil {
		return err
	}
	var accounts []struct{ Lifecycle string }
	if tx.Model(&entity.Account{}).Select("lifecycle").Where("id = ?", accountID).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&accounts).Error != nil || len(accounts) != 1 || accounts[0].Lifecycle != "active" {
		return ErrUnavailable
	}
	refs := []string{current.CurrentRef}
	if current.PreviousRef != nil {
		refs = append(refs, *current.PreviousRef)
	}
	values := make([][]byte, len(refs))
	sameOwner := false
	for i, ref := range refs {
		values[i], err = s.digest(ctx, ref, purpose, value)
		if err != nil {
			return err
		}
		var owners []struct{ AccountID string }
		if tx.Model(&entity.IdentityLookupClaim{}).Select("account_id").Where("purpose = ? AND key_ref = ? AND digest = ?", purpose, ref, values[i]).Find(&owners).Error != nil {
			return ErrUnavailable
		}
		if len(owners) > 0 {
			if owners[0].AccountID != accountID {
				return ErrConflict
			}
			sameOwner = true
		}
	}
	if sameOwner {
		return nil
	}
	var existing int64
	if tx.Model(&entity.IdentitySecret{}).Where("account_id = ? AND purpose = ?", accountID, purpose).Count(&existing).Error != nil {
		return ErrUnavailable
	}
	if existing != 0 {
		return ErrConflict
	}
	envelope, err := s.codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: accountID, Version: 1}, value)
	if err != nil {
		return ErrUnavailable
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return ErrUnavailable
	}
	if tx.Create(&entity.IdentitySecret{AccountID: accountID, Purpose: purpose, ContentVersion: 1, Envelope: json.RawMessage(encoded)}).Error != nil {
		return ErrUnavailable
	}
	for i, ref := range refs {
		if tx.Create(&entity.IdentityLookupClaim{Purpose: purpose, KeyRef: ref, Digest: values[i], AccountID: accountID}).Error != nil {
			return ErrConflict
		}
	}
	return nil
}
