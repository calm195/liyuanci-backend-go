package identity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// ReserveStudentNumber 在申请根事务中建立有效学号占用，驳回重提允许修正尚未通过的学号。
// 调用者须先取身份全局锁与账号guard；删除旧占用和写新密文/双代占用共同提交，失败保留原绑定。
func (s *Store) ReserveStudentNumber(ctx context.Context, tx *gorm.DB, account string, value []byte) (err error) {
	start := time.Now()
	defer func() {
		result := "pending_commit"
		if err != nil {
			result = "failure"
		}
		s.logger.Info("申请学号绑定完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if e := bindingTransaction(ctx, tx); e != nil {
		return e
	}
	tx = tx.WithContext(ctx)
	var owner entity.Account
	if e := tx.Select("lifecycle,verification").Where("id=?", account).Take(&owner).Error; e != nil || owner.Lifecycle != "active" || owner.Verification == "verified" || len(value) == 0 {
		return ErrUnavailable
	}
	// 原绑定入口负责首次写入及同值重放；仅明确冲突才进入受控更正分支。
	if e := s.bind(ctx, tx, account, "student_number", value); e == nil {
		return nil
	} else if !errors.Is(e, ErrConflict) {
		return e
	}
	state, e := state(tx, "student_number")
	if e != nil {
		return e
	}
	refs := []string{state.CurrentRef}
	if state.PreviousRef != nil {
		refs = append(refs, *state.PreviousRef)
	}
	digests := make([][]byte, len(refs))
	for i, ref := range refs {
		digests[i], e = s.digest(ctx, ref, "student_number", value)
		if e != nil {
			return e
		}
		var claims []entity.IdentityLookupClaim
		if e = tx.Select("account_id").Where("purpose=? AND key_ref=? AND digest=?", "student_number", ref, digests[i]).Find(&claims).Error; e != nil {
			return ErrUnavailable
		}
		for _, claim := range claims {
			if claim.AccountID != account {
				return ErrConflict
			}
		}
	}
	var secret entity.IdentitySecret
	if e = tx.Select("content_version").Where("account_id=? AND purpose=?", account, "student_number").Take(&secret).Error; e != nil || secret.ContentVersion < 1 {
		return ErrUnavailable
	}
	version := secret.ContentVersion + 1
	envelope, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: account, Version: uint64(version)}, value)
	if e != nil {
		return ErrUnavailable
	}
	encoded, e := json.Marshal(envelope)
	if e != nil {
		return ErrUnavailable
	}
	if e = tx.Where("account_id=? AND purpose=?", account, "student_number").Delete(&entity.IdentityLookupClaim{}).Error; e != nil {
		return ErrUnavailable
	}
	if e = tx.Model(&entity.IdentitySecret{}).Where("account_id=? AND purpose=?", account, "student_number").Updates(map[string]any{"content_version": version, "envelope": json.RawMessage(encoded)}).Error; e != nil {
		return ErrUnavailable
	}
	for i, ref := range refs {
		if e = tx.Create(&entity.IdentityLookupClaim{Purpose: "student_number", KeyRef: ref, Digest: digests[i], AccountID: account}).Error; e != nil {
			return ErrConflict
		}
	}
	return nil
}

// bindingTransaction 防止独立事务、普通连接或错误锁序进入身份占用写路径。
func bindingTransaction(ctx context.Context, tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return ErrUnavailable
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrUnavailable
	}
	var held bool
	if tx.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid() AND classid=0 AND objid=624109174 AND granted)").Scan(&held).Error != nil || !held {
		return ErrUnavailable
	}
	return nil
}
