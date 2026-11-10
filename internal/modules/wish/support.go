package wish

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// SortSupport 按公开心愿的累计支持数排序。
const SortSupport = "support"

// Support 是公开计数及当前会话的支持状态。
type Support struct {
	SupportCount int64 `json:"support_count"`
	Supported    bool  `json:"supported"`
}

func activeSupports(db *gorm.DB) *gorm.DB {
	return db.Table("public.wish_supports AS ws").
		Joins("JOIN public.accounts supporter ON supporter.id=ws.account_id AND supporter.lifecycle='active'")
}

func readSupport(db *gorm.DB, id, actor string) (out Support, err error) {
	err = activeSupports(db).
		Where("ws.wish_id=?", id).
		Select("count(*) AS support_count, count(*) FILTER (WHERE ws.account_id=NULLIF(?, '')::uuid)>0 AS supported", actor).
		Scan(&out).Error
	return
}

// SetSupport 将当前学生的支持设置为指定状态，重试不会重复计数或刷新支持时间。
func (s *Service) SetSupport(ctx context.Context, token, rid, id string, supported bool) (out Support, err error) {
	defer s.queryDone("wish.support", rid, time.Now(), &err)
	if !validWishID(id) {
		return out, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		// 先锁定心愿，再读取公开资格。串行化同一心愿的写入及返回计数。
		var locked string
		if e := db.Raw("SELECT id FROM public.wishes WHERE id=? FOR UPDATE", id).Scan(&locked).Error; e != nil {
			return e
		}
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		actor, e := currentReader(ctx, db, token, now)
		if e != nil {
			return e
		}
		if locked == "" {
			return command.ErrNotFound
		}
		a, e := readAggregate(ctx, db, id, false)
		if e != nil {
			return e
		}
		if _, e = s.publicContent(rid, now, a); e != nil {
			return e
		}
		if supported {
			e = db.Exec("INSERT INTO public.wish_supports(wish_id,account_id,created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING", id, actor, now).Error
		} else {
			e = db.Where("wish_id=? AND account_id=?", id, actor).Delete(&entity.WishSupport{}).Error
		}
		if e != nil {
			return e
		}
		out, e = readSupport(db, id, actor)
		return e
	})
	return out, readError(err)
}
