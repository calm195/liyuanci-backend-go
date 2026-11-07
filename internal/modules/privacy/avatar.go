package privacy

import (
	"context"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// AvatarObjects 只允许移除头像命名空间中的私有对象。
type AvatarObjects interface {
	RemoveAvatar(context.Context, string) error
}

func (s *Service) cleanNextAvatar(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	var rows []entity.AccountAvatar
	// 先持久认领并设置重试时间。关闭进程或外部失败时仍保留对象引用，
	// 外部删除无需持有数据库锁。已进入清理的预约不能再次上传。
	err = s.db.WithContext(ctx).Raw(`
WITH candidate AS (
 SELECT a.id FROM public.account_avatars a
 WHERE a.removable_after <= clock_timestamp()
   AND (a.upload_until IS NULL OR a.upload_until <= clock_timestamp())
   AND (a.removed_at IS NULL OR a.owner_id IS NULL)
   AND NOT EXISTS (SELECT 1 FROM public.accounts u WHERE u.avatar_id = a.id)
 ORDER BY a.removable_after, a.id
 LIMIT 1 FOR UPDATE OF a SKIP LOCKED
)
UPDATE public.account_avatars a
SET cleanup_started_at = COALESCE(a.cleanup_started_at, clock_timestamp()),
    removable_after = clock_timestamp() + (? * interval '1 millisecond')
FROM candidate c WHERE a.id = c.id
RETURNING a.*`, s.cfg.RetryDelay.Milliseconds()).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return err
	}
	defer s.done("avatar.cleanup", "", time.Now(), &err)
	row := rows[0]
	if row.RemovedAt == nil {
		if err = s.cfg.AvatarObjects.RemoveAvatar(ctx, row.ObjectKey); err != nil {
			return err
		}
	}
	db := s.db.WithContext(ctx).Model(&entity.AccountAvatar{}).Where("id=?", row.ID)
	if row.OwnerID == nil {
		return db.Where("owner_id IS NULL").Delete(&entity.AccountAvatar{}).Error
	}
	// 保留已有账号的最小清理回执，原幂等键重放时继续返回当前头像。
	return db.Update("removed_at", time.Now().UTC()).Error
}
