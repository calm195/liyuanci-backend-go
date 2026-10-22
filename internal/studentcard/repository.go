package studentcard

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func createAsset(ctx context.Context, db *gorm.DB, asset entity.Asset, payload []byte, mime string) error {
	if e := db.WithContext(ctx).Create(&asset).Error; e != nil {
		return e
	}
	return db.WithContext(ctx).Create(&entity.StudentCardPayload{AssetID: asset.ID, Envelope: payload, MIME: mime}).Error
}
func readAsset(ctx context.Context, db *gorm.DB, id string) (entity.Asset, error) {
	var out entity.Asset
	e := db.WithContext(ctx).Where("id=? AND purpose=?", id, "student_card").Take(&out).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return out, command.ErrNotFound
	}
	return out, e
}
func readWork(ctx context.Context, db *gorm.DB, id string, lock bool) (entity.WorkItem, error) {
	var out entity.WorkItem
	query := db.WithContext(ctx).Where("id=? AND kind=? AND target_kind=?", id, Route().Kind, Route().TargetKind)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	e := query.Take(&out).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return out, command.ErrNotFound
	}
	return out, e
}
func now(ctx context.Context, db *gorm.DB) (time.Time, error) {
	var out time.Time
	e := db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&out).Error
	return out, e
}

// authorizeStudent 在已有事务中按账号guard→学生会话顺序读取，不接收后台主体。
func authorizeStudent(ctx context.Context, db *gorm.DB, token string, timeout time.Duration) (string, error) {
	if token == "" {
		return "", command.ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	var initial entity.Session
	if e := db.WithContext(ctx).Select("account_id").Where("token_hash=? AND subject_kind=?", digest[:], "student").Take(&initial).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return "", command.ErrUnauthenticated
		}
		return "", e
	}
	if initial.AccountID == nil {
		return "", command.ErrUnauthenticated
	}
	tx, e := command.LockPlan(ctx, db, command.Discovery{Accounts: []string{*initial.AccountID}}, timeout)
	if e != nil {
		return "", e
	}
	guard, ok := tx.Guard(*initial.AccountID)
	if !ok || guard.Lifecycle != "active" {
		return "", command.ErrUnauthenticated
	}
	var session entity.Session
	if e = db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash=? AND subject_kind=?", digest[:], "student").Take(&session).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return "", command.ErrUnauthenticated
		}
		return "", e
	}
	at, e := now(ctx, db)
	if e != nil {
		return "", e
	}
	if session.AccountID == nil || *session.AccountID != guard.ID || session.AuthEpoch != guard.AuthEpoch || session.RevokedAt != nil || !session.ExpiresAt.After(at) {
		return "", command.ErrUnauthenticated
	}
	return guard.ID, nil
}

func setTimeout(ctx context.Context, db *gorm.DB, timeout time.Duration) error {
	return db.WithContext(ctx).Exec("SELECT set_config('lock_timeout',?,true)", strconv.FormatInt(timeout.Milliseconds(), 10)+"ms").Error
}

func finishWork(ctx context.Context, db *gorm.DB, work entity.WorkItem, status string) error {
	if e := db.WithContext(ctx).Model(&entity.WorkItem{}).Where("id=?", work.ID).Updates(map[string]any{"status": status, "lease_until": nil, "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	receipt := entity.ConsumerReceipt{ConsumerKind: "student_card.scan", OperationID: work.OperationID}
	// 领域worker仅拥有回执键读取权限，不能让GORM默认回读完成时间。
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}, clause.Returning{Columns: []clause.Column{{Name: "operation_id"}}}).Create(&receipt).Error
}
