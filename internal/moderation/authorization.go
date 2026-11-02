package moderation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"slices"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// transaction 为查询和审核操作设置统一期限与可重复读快照，回调负责具体岗位授权。
func (s *Service) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	bounded, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(bounded).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}

// authorize 锁定当前后台主体及会话，每次复核数据库角色，学生和危机单独角色不能进入普通审核。
func (s *Service) authorize(ctx context.Context, db *gorm.DB, token string, plan command.Discovery) (string, error) {
	return s.authorizeRoles(ctx, db, token, plan, []string{"super_admin", "review_operator"})
}

// authorizeRoles 在主体锁后重读会话、认证代次和当前角色，不把连接池权限当作业务授权。
func (s *Service) authorizeRoles(ctx context.Context, db *gorm.DB, token string, plan command.Discovery, allowed []string) (string, error) {
	if token == "" {
		return "", command.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var session entity.Session
	if e := db.Where("token_hash=? AND subject_kind='admin'", hash[:]).Take(&session).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return "", command.ErrUnauthenticated
		}
		return "", e
	}
	if session.AdminID == nil {
		return "", command.ErrUnauthenticated
	}
	id := *session.AdminID
	plan.Admins = append(plan.Admins, id)
	tx, e := command.LockPlan(ctx, db, plan, s.cfg.Command.LockTimeout)
	if e != nil {
		return "", e
	}
	var admin entity.AdminAccount
	if e = db.Where("id=?", id).Take(&admin).Error; e != nil {
		return "", e
	}
	if admin.Status != "active" {
		return "", command.ErrUnauthenticated
	}
	if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", session.ID).Take(&session).Error; e != nil {
		return "", e
	}
	if session.RevokedAt != nil || session.AuthEpoch != admin.AuthEpoch || !session.ExpiresAt.After(tx.Now()) {
		return "", command.ErrUnauthenticated
	}
	var roles []string
	if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", id).Pluck("role", &roles).Error; e != nil {
		return "", e
	}
	if !slices.ContainsFunc(roles, func(r string) bool { return slices.Contains(allowed, r) }) {
		return "", command.ErrForbidden
	}
	return id, nil
}

func audit(db *gorm.DB, actor, subject, purpose string) error {
	return db.Create(&entity.AccessAudit{ID: uuid.NewString(), ActorID: actor, SubjectRef: subject, Purpose: purpose, Outcome: "allowed"}).Error
}
