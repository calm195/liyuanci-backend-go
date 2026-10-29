package safety

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// UserRestrictionState 同时保留封禁和禁言状态，解除一种不会错误宣称全部恢复。
type UserRestrictionState struct {
	UserID         string     `json:"user_id"`
	AccountVersion int64      `json:"account_version"`
	Banned         bool       `json:"banned"`
	Muted          bool       `json:"muted"`
	MutedUntil     *time.Time `json:"muted_until"`
	AsOf           time.Time  `json:"as_of"`
	AllowedActions []string   `json:"allowed_actions"`
}

func isUserRestriction(op string) bool {
	return slices.Contains([]string{"user_ban", "user_unban", "user_mute", "user_unmute"}, op)
}
func activeUserPenalties(db *gorm.DB, id, kind string, now time.Time) *gorm.DB {
	return db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind=? AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", id, kind, now, now)
}
func userRestrictionState(ctx context.Context, db *gorm.DB, id string, now time.Time) (out UserRestrictionState, err error) {
	db = db.WithContext(ctx)
	var account entity.Account
	if err = found(db.Select("id,version").Where("id=? AND lifecycle='active'", id).Take(&account).Error); err != nil {
		return
	}
	var n int64
	if err = activeUserPenalties(db, id, "ban", now).Count(&n).Error; err != nil {
		return
	}
	out = UserRestrictionState{UserID: id, AccountVersion: account.Version, Banned: n > 0, AsOf: now.UTC(), AllowedActions: []string{}}
	var mute struct{ Until *time.Time }
	if err = activeUserPenalties(db, id, "mute", now).Select("max(ends_at) AS until").Scan(&mute).Error; err != nil {
		return
	}
	out.MutedUntil = utcTime(mute.Until)
	out.Muted = out.MutedUntil != nil
	if out.Banned {
		out.AllowedActions = append(out.AllowedActions, "unban")
	} else {
		out.AllowedActions = append(out.AllowedActions, "ban")
	}
	if out.Muted {
		out.AllowedActions = append(out.AllowedActions, "unmute")
	} else {
		out.AllowedActions = append(out.AllowedActions, "mute")
	}
	return
}

// UserRestrictions 只允许超级管理员预览直接处置，原三角色查询权限不变。
func (s *Service) UserRestrictions(ctx context.Context, token, rid, id string) (out UserRestrictionState, err error) {
	defer s.done("admin_user_restrictions", rid, time.Now(), &err)
	defer func() {
		if err != nil {
			out = UserRestrictionState{}
		}
	}()
	id, err = NormalizeUserID(id)
	if err != nil {
		return
	}
	err = s.usersRead(ctx, token, id, func(db *gorm.DB, actor string, roles []string, now time.Time) error {
		if !slices.Contains(roles, "super_admin") {
			return command.ErrForbidden
		}
		var e error
		out, e = userRestrictionState(ctx, db, id, now)
		if e != nil {
			return e
		}
		return audit(db, actor, id, "admin_user_restrictions")
	})
	return
}
func discoverUserRestriction(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	p, err = accountPlan(ctx, db, id)
	if err != nil {
		return
	}
	var ids []string
	if err = db.WithContext(ctx).Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('ban','mute') AND revoked_at IS NULL", id).Pluck("id", &ids).Error; err != nil {
		return
	}
	for _, pid := range ids {
		p.Resources = append(p.Resources, command.Resource{Kind: command.Penalty, ID: pid})
	}
	return
}

// applyUserRestriction 复用处罚与账号锁；只撤当前类型，不触碰内容、危机或隐私屏障。
func (s *Service) applyUserRestriction(ctx context.Context, tx *command.Tx, op, id string, in Input) (string, error) {
	db := tx.DB().WithContext(ctx)
	action := strings.TrimPrefix(op, "user_")
	state, e := userRestrictionState(ctx, db, id, tx.Now())
	if e != nil {
		return "", e
	}
	if !slices.Contains(state.AllowedActions, action) {
		return "", command.ErrConflict
	}
	if action == "ban" || action == "mute" {
		pid := uuid.NewString()
		level := "severe"
		var ends *time.Time
		if action == "mute" {
			level = "general"
			v := tx.Now().Add(168 * time.Hour)
			ends = &v
		}
		cipher, e := s.encrypt(ctx, "penalty_reason", pid, in.Reason)
		if e != nil {
			return "", e
		}
		p := entity.Penalty{ID: pid, AccountID: id, SourceKind: "direct", Level: level, ActionKind: action, ViolationRef: pid, ContentVersion: 1, StartsAt: tx.Now(), EndsAt: ends, ReasonRef: uuid.NewString(), ReasonCipher: cipher}
		if e = db.Create(&p).Error; e != nil {
			return "", e
		}
		if e = ApplyPenaltyEffects(ctx, tx, id); e != nil {
			return "", e
		}
		if e = notify(db, id, "penalty_notice", pid); e != nil {
			return "", e
		}
	} else {
		kind := "ban"
		if action == "unmute" {
			kind = "mute"
		}
		var rows []entity.Penalty
		if e = activeUserPenalties(db, id, kind, tx.Now()).Find(&rows).Error; e != nil {
			return "", e
		}
		for _, p := range rows {
			cipher, e := s.encrypt(ctx, "penalty_revoke_reason", p.ID, in.Reason)
			if e != nil {
				return "", e
			}
			if e = db.Model(&entity.Penalty{}).Where("id=?", p.ID).Updates(map[string]any{"revoked_at": tx.Now(), "version": gorm.Expr("version+1"), "revoke_reason_cipher": cipher, "revoked_by": tx.Actor().ID}).Error; e != nil {
				return "", e
			}
			if e = audit(db, tx.Actor().ID, p.ID, "user_restriction."+action); e != nil {
				return "", e
			}
		}
		if e = db.Model(&entity.Account{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error; e != nil {
			return "", e
		}
	}
	return id, audit(db, tx.Actor().ID, id, "user_restriction."+action)
}
