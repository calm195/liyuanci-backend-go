package privacy

import (
	"context"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// WishImageObjects 仅移除心愿图片的独立密文对象命名空间。
type WishImageObjects interface {
	RemoveWishImage(context.Context, string) error
}

func (s *Service) cleanNextWishImage(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	var rows []entity.WishImage
	// 只回收无当前正文引用的图片；与绑定操作以图片行锁串行，禁止清理认领后再次引用。
	err = s.db.WithContext(ctx).Raw(`WITH candidate AS (
 SELECT i.id FROM public.wish_images i
 WHERE i.removable_after<=clock_timestamp()
 AND (i.upload_until IS NULL OR i.upload_until<=clock_timestamp())
 AND (i.removed_at IS NULL OR i.owner_id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM public.wish_version_images r JOIN public.wishes w ON w.id=r.wish_id
  JOIN public.accounts a ON a.id=w.owner_id AND a.lifecycle='active'
  WHERE r.image_id=i.id AND r.content_version=w.current_content_version AND w.draft_deleted_at IS NULL)
 ORDER BY i.removable_after,i.id LIMIT 1 FOR UPDATE OF i SKIP LOCKED
 ) UPDATE public.wish_images i SET cleanup_started_at=COALESCE(i.cleanup_started_at,clock_timestamp()),
 removable_after=clock_timestamp()+(? * interval '1 millisecond')
 FROM candidate c WHERE i.id=c.id RETURNING i.*`, s.cfg.RetryDelay.Milliseconds()).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return err
	}
	defer s.done("wish_image.cleanup", "", time.Now(), &err)
	row := rows[0]
	if row.RemovedAt == nil {
		if err = s.cfg.WishImageObjects.RemoveWishImage(ctx, row.ObjectKey); err != nil {
			return err
		}
	}
	db := s.db.WithContext(ctx).Model(&entity.WishImage{}).Where("id=?", row.ID)
	if row.OwnerID == nil {
		return db.Where("owner_id IS NULL AND NOT EXISTS(SELECT 1 FROM public.wish_version_images r WHERE r.image_id=wish_images.id)").Delete(&entity.WishImage{}).Error
	}
	return db.Updates(map[string]any{"removed_at": time.Now().UTC(), "upload_cipher": nil}).Error
}
