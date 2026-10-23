package moderation

import (
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// acceptedRecheck 只复核尚存匹配的已接受留言，不使它重新进入可同意队列。
// 账号和心愿锁由调用方持有，关系结束、双方代次、处罚、拉黑和屏障仍会取消复核。
func acceptedRecheck(db *gorm.DB, row current) (bool, error) {
	a := row.application
	if a == nil || !row.task.CrisisRecheck || a.Status != "accepted" || row.account.Lifecycle != "active" || row.account.Verification != "verified" || row.task.SubjectEpoch != row.account.AuthEpoch || a.OwnerEpoch == nil || row.task.RestrictionEpoch == nil || *row.task.RestrictionEpoch != row.wish.RestrictionEpoch || a.WishContentVersion != row.wish.CurrentContentVersion || row.wish.DraftDeletedAt != nil {
		return false, nil
	}
	var owner entity.Account
	if e := db.Where("id=?", row.wish.OwnerID).Take(&owner).Error; e != nil {
		return false, e
	}
	if owner.Lifecycle != "active" || owner.AuthEpoch != *a.OwnerEpoch {
		return false, nil
	}
	var n int64
	if e := db.Model(&entity.Match{}).Where("application_id=? AND wish_id=? AND requester_id=? AND helper_id=? AND ended_at IS NULL", a.ID, row.wish.ID, owner.ID, a.ApplicantID).Count(&n).Error; e != nil || n != 1 {
		return false, e
	}
	if e := db.Raw(`SELECT
 (SELECT count(*) FROM public.penalties WHERE account_id IN (?,?) AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp()))+
 (SELECT count(*) FROM public.blocks WHERE (owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?))+
 (SELECT count(*) FROM public.content_restrictions r JOIN public.content_registry c ON c.id=r.content_id WHERE r.active AND ((c.kind='wish' AND c.entity_id=?) OR (c.kind='application' AND c.entity_id=?)))+
 (SELECT count(*) FROM public.privacy_holds WHERE target_kind='wish' AND target_id=? AND (status='active' OR hold_epoch>?))`, owner.ID, a.ApplicantID, owner.ID, a.ApplicantID, a.ApplicantID, owner.ID, row.wish.ID, a.ID, row.wish.ID, row.task.HoldEpoch).Scan(&n).Error; e != nil {
		return false, e
	}
	return n == 0, nil
}
