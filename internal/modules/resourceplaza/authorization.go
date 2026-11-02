package resourceplaza

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// actor 只在完成会话、资格和岗位复核的事务内使用，Now来自持锁后的数据库时间。
type actor struct {
	ID           string
	Admin, Muted bool
	Now          time.Time
}

// transaction 先锁主体重验当前会话，再执行领域锁，避免对象存在性泄露。
// 写事务允许发现后重读并重试；读事务保持同一快照。调用方不得缓存actor跨请求复用。
func (s *Service) transaction(ctx context.Context, token string, admin, write bool, run func(*gorm.DB, actor) error) error {
	if token == "" {
		return command.ErrUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	isolation := sql.LevelReadCommitted
	if !write {
		isolation = sql.LevelRepeatableRead
	}
	var e error
	for attempt := 0; attempt < 3; attempt++ {
		e = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			h := sha256.Sum256([]byte(token))
			var session entity.Session
			e := db.Where("token_hash=? AND revoked_at IS NULL AND expires_at>clock_timestamp()", h[:]).Take(&session).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				var other bool
				fn := "is_live_admin_session"
				if admin {
					fn = "is_live_student_session"
				}
				if e = db.Raw("SELECT public."+fn+"(decode(?,'hex'))", hex.EncodeToString(h[:])).Scan(&other).Error; e != nil {
					return e
				}
				if other {
					return command.ErrForbidden
				}
				return command.ErrUnauthenticated
			}
			if e != nil {
				return e
			}
			if (session.SubjectKind == "admin") != admin {
				return command.ErrForbidden
			}
			p := command.Discovery{}
			if admin {
				p.Admins = []string{session.SubjectID}
			} else {
				p.Accounts = []string{session.SubjectID}
			}
			guard, e := command.LockPlan(ctx, db, p, s.cfg.Command.LockTimeout)
			if e != nil {
				return e
			}
			now := guard.Now()
			var epoch int64
			if admin {
				var v entity.AdminAccount
				if e = db.Select("id,status,auth_epoch").Where("id=? AND status='active'", session.SubjectID).Take(&v).Error; e != nil {
					return command.ErrUnauthenticated
				}
				epoch = v.AuthEpoch
			} else {
				g, ok := guard.Guard(session.SubjectID)
				if !ok || g.Lifecycle != "active" {
					return command.ErrUnauthenticated
				}
				epoch = g.AuthEpoch
			}
			if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND revoked_at IS NULL AND expires_at>? AND auth_epoch=?", session.ID, now, epoch).Take(&session).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return command.ErrUnauthenticated
				}
				return e
			}
			a := actor{ID: session.SubjectID, Admin: admin, Now: now}
			if admin {
				var roles []string
				if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", a.ID).Pluck("role", &roles).Error; e != nil {
					return e
				}
				if !slices.Contains(roles, "review_operator") && !slices.Contains(roles, "super_admin") {
					return command.ErrForbidden
				}
			} else {
				var kinds []string
				if e = db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('ban','mute') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", a.ID, now, now).Pluck("action_kind", &kinds).Error; e != nil {
					return e
				}
				if slices.Contains(kinds, "ban") {
					return command.ErrForbidden
				}
				g, _ := guard.Guard(a.ID)
				if g.Verification != "verified" {
					return command.ErrVerificationRequired
				}
				a.Muted = slices.Contains(kinds, "mute")
				if write && a.Muted {
					return command.ErrForbidden
				}
			}
			if e = db.Exec("SELECT set_config('app.resource_actor',?,true)", a.ID).Error; e != nil {
				return e
			}
			return run(db, a)
		}, &sql.TxOptions{Isolation: isolation})
		e = normalizeError(e)
		if !errors.Is(e, command.ErrRetry) {
			return e
		}
	}
	return e
}
