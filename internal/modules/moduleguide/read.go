package moduleguide

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

// ReadAdmin 仅向当前有效超管会话返回说明草稿。
func (s *Service) ReadAdmin(ctx context.Context, token, rid string) (out AdminGuide, err error) {
	err = s.read(ctx, token, rid, true, func(db *gorm.DB, _ string) error {
		value, e := row(ctx, db, false)
		if e == nil {
			out, e = adminProjection(value)
		}
		return e
	})
	if err != nil {
		out = AdminGuide{}
	}
	return out, err
}

// Read 允许有效学生会话读取说明，仅返回其本人的确认状态。
func (s *Service) Read(ctx context.Context, token, rid string) (out Guide, err error) {
	err = s.read(ctx, token, rid, false, func(db *gorm.DB, id string) error {
		var e error
		out, e = studentProjection(ctx, db, id)
		return e
	})
	if err != nil {
		out = Guide{}
	}
	return out, err
}

func (s *Service) read(ctx context.Context, token, rid string, admin bool, run func(*gorm.DB, string) error) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		s.log.Info("模块说明读取完成", zap.String("request_id", rid), zap.Bool("admin", admin), zap.String("result", result), zap.Duration("duration", time.Since(start)))
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
			return command.ErrUnavailable
		}
		if (session.SubjectKind == "admin") != admin {
			return command.ErrForbidden
		}
		plan := command.Discovery{}
		if admin {
			plan.Admins = []string{session.SubjectID}
		} else {
			plan.Accounts = []string{session.SubjectID}
		}
		locked, e := command.LockPlan(ctx, db, plan, s.cfg.LockTimeout)
		if e != nil {
			return e
		}
		var epoch int64
		if admin {
			var account entity.AdminAccount
			if e = db.Where("id=? AND status='active'", session.SubjectID).Take(&account).Error; e != nil {
				return command.ErrUnauthenticated
			}
			epoch = account.AuthEpoch
			var count int64
			if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=? AND role='super_admin'", session.SubjectID).Count(&count).Error; e != nil {
				return e
			}
			if count == 0 {
				return command.ErrForbidden
			}
		} else {
			guard, ok := locked.Guard(session.SubjectID)
			if !ok || guard.Lifecycle != "active" {
				return command.ErrUnauthenticated
			}
			epoch = guard.AuthEpoch
		}
		if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND revoked_at IS NULL AND expires_at>? AND auth_epoch=?", session.ID, locked.Now(), epoch).Take(&session).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrUnauthenticated
			}
			return e
		}
		return run(db, session.SubjectID)
	})
	for _, known := range []error{command.ErrUnauthenticated, command.ErrForbidden, command.ErrUnavailable, command.ErrRetry} {
		if errors.Is(err, known) {
			return known
		}
	}
	if err != nil {
		return command.ErrUnavailable
	}
	return nil
}
