package contentinteraction

import (
	"context"
	"errors"
	"reflect"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// TransactionResolver 复用各领域公开投影，在已持锁事务内复核当前目标资格。
type TransactionResolver func(context.Context, *gorm.DB, time.Time, string, string, string) (Target, error)

func discoverTarget(ctx context.Context, db *gorm.DB, kind, id string) (p command.Discovery, err error) {
	db = db.WithContext(ctx)
	switch kind {
	case "wish":
		var row entity.Wish
		err = db.Select("id,owner_id").Where("id=?", id).Take(&row).Error
		p.Accounts = []string{row.OwnerID}
		p.Resources = []command.Resource{{Kind: command.Wish, ID: id}}
	case "whisper_post":
		var row entity.WhisperPost
		err = db.Select("id,owner_id").Where("id=?", id).Take(&row).Error
		p.Accounts = []string{row.OwnerID}
		p.Resources = []command.Resource{{Kind: command.WhisperPost, ID: id}}
	case "resource":
		var row entity.ResourceMaterial
		err = db.Select("id,category_id").Where("id=?", id).Take(&row).Error
		p.Resources = []command.Resource{{Kind: command.ResourceCategory, ID: row.CategoryID}, {Kind: command.ResourceMaterial, ID: id}}
	default:
		return p, command.ErrInvalid
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

// withTargetWrite 将浏览事件写入与当前权限复核放在同一锁区间，注销/下架无法在其中穿插。
func (s *Service) withTargetWrite(ctx context.Context, token, kind, id string, run func(*gorm.DB, string, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		actor := ""
		if token != "" {
			var e error
			actor, e = account(ctx, db, token)
			if e != nil {
				return e
			}
		}
		p, e := discoverTarget(ctx, db, kind, id)
		if e != nil {
			return e
		}
		if actor != "" {
			p.Accounts = append(p.Accounts, actor)
		}
		p, e = command.NormalizePlan(p)
		if e != nil {
			return e
		}
		guard, e := command.LockPlan(ctx, db, p, s.cfg.LockTimeout)
		if e != nil {
			return e
		}
		fresh, e := discoverTarget(ctx, db, kind, id)
		if e != nil {
			return e
		}
		if actor != "" {
			fresh.Accounts = append(fresh.Accounts, actor)
		}
		fresh, e = command.NormalizePlan(fresh)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(p, fresh) {
			return command.ErrRetry
		}
		if actor != "" {
			current, e := account(ctx, db, token)
			if e != nil {
				return e
			}
			if actor != current {
				return command.ErrUnauthenticated
			}
		}
		if _, e = s.resolveTx(ctx, db, guard.Now(), actor, kind, id); e != nil {
			return e
		}
		return run(db, actor, guard.Now())
	})
}
