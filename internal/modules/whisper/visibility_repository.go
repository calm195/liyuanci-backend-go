package whisper

import (
	"context"

	"gorm.io/gorm"
)

type contentFlags struct{ Privacy, Isolated, Restricted bool }

// readContentFlags 为帖子和评论装载统一屏障；公开资格与交互权限仍由各自领域规则复核。
func readContentFlags(ctx context.Context, db *gorm.DB, kind, id string) (flags contentFlags, err error) {
	err = db.WithContext(ctx).Raw(`SELECT
 EXISTS(SELECT 1 FROM public.privacy_holds
        WHERE target_kind=? AND target_id=? AND status='active') AS privacy,
 EXISTS(SELECT 1 FROM public.content_restrictions x
        JOIN public.content_registry c ON c.id=x.content_id
        WHERE c.kind=? AND c.entity_id=? AND x.active AND x.kind='crisis') AS isolated,
 EXISTS(SELECT 1 FROM public.content_restrictions x
        JOIN public.content_registry c ON c.id=x.content_id
        WHERE c.kind=? AND c.entity_id=? AND x.active) AS restricted`,
		kind, id, kind, id, kind, id).Scan(&flags).Error
	return
}
