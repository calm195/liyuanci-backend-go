package dictionary

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ReadAdmin 在管理员锁内复核账号、岗位、会话代次和有效期，再读取一致快照。
func (s *Service) ReadAdmin(ctx context.Context, token, rid string) (out Snapshot, err error) {
	err = s.readAdmin(ctx, token, rid, func(db *gorm.DB) error {
		var e error
		out, e = snapshot(db.Statement.Context, db)
		return e
	})
	if err != nil {
		out = Snapshot{}
	}
	return out, err
}

func (s *Service) readAdmin(ctx context.Context, token, rid string, read func(*gorm.DB) error) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		s.log.Info("后台展示字典读取完成", zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if token == "" {
		return command.ErrUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		hash := sha256.Sum256([]byte(token))
		var session entity.Session
		e := db.Where("token_hash=?", hash[:]).Take(&session).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return command.ErrUnauthenticated
		}
		if e != nil {
			return e
		}
		if session.SubjectKind != "admin" {
			return command.ErrForbidden
		}
		locked, e := command.LockPlan(ctx, db, command.Discovery{Admins: []string{session.SubjectID}}, s.cfg.LockTimeout)
		if e != nil {
			return e
		}
		var account entity.AdminAccount
		if e = db.Where("id=? AND status='active'", session.SubjectID).Take(&account).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrUnauthenticated
			}
			return e
		}
		var count int64
		if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", session.SubjectID).Count(&count).Error; e != nil {
			return e
		}
		if count == 0 {
			return command.ErrForbidden
		}
		if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND revoked_at IS NULL AND expires_at>? AND auth_epoch=?", session.ID, locked.Now(), account.AuthEpoch).Take(&session).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrUnauthenticated
			}
			return e
		}
		return read(db)
	})
	if err != nil {
		for _, known := range []error{command.ErrUnauthenticated, command.ErrForbidden, command.ErrRetry} {
			if errors.Is(err, known) {
				return known
			}
		}
		return command.ErrUnavailable
	}
	return nil
}
