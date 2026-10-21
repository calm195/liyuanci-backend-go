package verification

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func latest(db *gorm.DB, account string) (entity.IdentityReview, error) {
	var r entity.IdentityReview
	e := db.Where("account_id=?", account).Order("version DESC").Limit(1).Take(&r).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return r, command.ErrNotFound
	}
	return r, e
}
func review(db *gorm.DB, id string) (entity.IdentityReview, error) {
	var r entity.IdentityReview
	e := db.Where("id=?", id).Take(&r).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return r, command.ErrNotFound
	}
	return r, e
}
func asset(db *gorm.DB, id, owner string) (entity.Asset, error) {
	var a entity.Asset
	e := db.Where("id=? AND owner_id=? AND purpose=?", id, owner, "student_card").Take(&a).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return a, command.ErrNotFound
	}
	if e == nil {
		if a.Status == "deleting" || a.Status == "deleted" {
			return a, command.ErrNotFound
		}
		if a.Status != "active" || a.ScanStatus != "approved" {
			return a, ErrAssetNotReady
		}
	}
	return a, e
}
func audit(db *gorm.DB, actor, subject, purpose, outcome string) error {
	return db.Create(&entity.AccessAudit{ID: uuid.NewString(), ActorID: actor, SubjectRef: subject, Purpose: purpose, Outcome: outcome}).Error
}

// authorize 在一个根事务内依次锁后台主体、业务账号、资源及会话，撤权和注销不能被缓存绕过。
func (s *Service) authorize(ctx context.Context, db *gorm.DB, token, kind string, plan command.Discovery) (string, error) {
	hash := sha256.Sum256([]byte(token))
	var initial entity.Session
	if token == "" {
		return "", command.ErrUnauthenticated
	}
	if e := db.WithContext(ctx).Where("token_hash=? AND subject_kind=?", hash[:], kind).Take(&initial).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return "", command.ErrUnauthenticated
		}
		return "", e
	}
	var id string
	if kind == "student" {
		if initial.AccountID == nil {
			return "", command.ErrUnauthenticated
		}
		id = *initial.AccountID
		plan.Accounts = append(plan.Accounts, id)
	} else {
		if initial.AdminID == nil {
			return "", command.ErrUnauthenticated
		}
		id = *initial.AdminID
		plan.Admins = append(plan.Admins, id)
	}
	tx, e := command.LockPlan(ctx, db, plan, s.cfg.Command.LockTimeout)
	if e != nil {
		return "", e
	}
	var epoch int64
	if kind == "student" {
		g, ok := tx.Guard(id)
		if !ok || g.Lifecycle != "active" {
			return "", command.ErrUnauthenticated
		}
		epoch = g.AuthEpoch
	} else {
		var a entity.AdminAccount
		if e = db.Select("status,auth_epoch").Where("id=?", id).Take(&a).Error; e != nil {
			return "", e
		}
		if a.Status != "active" {
			return "", command.ErrUnauthenticated
		}
		epoch = a.AuthEpoch
		var roles []string
		if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", id).Pluck("role", &roles).Error; e != nil {
			return "", e
		}
		if !slices.Contains(roles, "review_operator") && !slices.Contains(roles, "super_admin") {
			return "", command.ErrForbidden
		}
	}
	var session entity.Session
	if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash=? AND subject_kind=?", hash[:], kind).Take(&session).Error; e != nil {
		return "", e
	}
	if session.AuthEpoch != epoch || session.RevokedAt != nil || !session.ExpiresAt.After(tx.Now()) {
		return "", command.ErrUnauthenticated
	}
	for _, account := range plan.Accounts {
		g, ok := tx.Guard(account)
		if !ok || g.Lifecycle != "active" {
			return "", command.ErrNotFound
		}
	}
	return id, nil
}
func (s *Service) lockedCase(ctx context.Context, db *gorm.DB, token, id string) (string, entity.IdentityReview, error) {
	r, e := review(db, id)
	if e != nil {
		return "", r, e
	}
	actor, e := s.authorize(ctx, db, token, "admin", command.Discovery{Accounts: []string{r.AccountID}, Resources: []command.Resource{{Kind: command.Asset, ID: r.AssetID}, {Kind: command.IdentityReview, ID: id}}})
	if e != nil {
		return "", r, e
	}
	current, e := review(db, id)
	if e != nil {
		return "", r, e
	}
	if current.AccountID != r.AccountID || current.AssetID != r.AssetID {
		return "", r, command.ErrConflict
	}
	if len(current.DataCipher) == 0 || !slices.Contains([]string{"pending", "approved", "rejected"}, current.Status) {
		return "", r, command.ErrNotFound
	}
	return actor, current, nil
}

func createReview(db *gorm.DB, row *entity.IdentityReview) error { return db.Create(row).Error }
func updateReview(db *gorm.DB, id string, updates map[string]any) error {
	return db.Model(&entity.IdentityReview{}).Where("id=?", id).Updates(updates).Error
}
func setVerification(db *gorm.DB, account, status string) error {
	return db.Model(&entity.Account{}).Where("id=?", account).Updates(map[string]any{"verification": status, "version": gorm.Expr("version+1")}).Error
}
func createName(db *gorm.DB, account string, cipher json.RawMessage) error {
	return db.Create(&entity.IdentityName{AccountID: account, ContentVersion: 1, NameCipher: cipher}).Error
}
func listReviews(db *gorm.DB, after string, limit int) ([]entity.IdentityReview, error) {
	var rows []entity.IdentityReview
	q := db.Model(&entity.IdentityReview{}).Select("identity_reviews.id,identity_reviews.version,identity_reviews.status").Joins("JOIN public.accounts ON accounts.id=identity_reviews.account_id AND accounts.lifecycle='active'").Where("identity_reviews.data_cipher IS NOT NULL AND identity_reviews.status=?", "pending")
	if after != "" {
		q = q.Where("identity_reviews.id > ?", after)
	}
	e := q.Order("identity_reviews.id").Limit(limit + 1).Find(&rows).Error
	return rows, e
}
