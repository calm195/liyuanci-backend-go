package safety

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// DiscoverPenaltyEffects 为跨模块处罚发现受影响的旧业务关系；调用者须补充其领域资源。
func DiscoverPenaltyEffects(ctx context.Context, db *gorm.DB, accountID string) (command.Discovery, error) {
	return accountPlan(ctx, db, accountID)
}

// ApplyPenaltyEffects 复用账号限制生命周期，撤销关系和待审资格但保留本人申诉会话。
// 调用方已按完整参与者集合持锁，完成日志由处罚命令边界统一记录。
func ApplyPenaltyEffects(ctx context.Context, tx *command.Tx, id string) error {
	db := tx.DB().WithContext(ctx)
	if e := cancelRelationships(ctx, tx, id, ""); e != nil {
		return e
	}
	if e := db.Model(&entity.Wish{}).Where("owner_id=? AND draft_deleted_at IS NULL AND state='published'", id).Updates(map[string]any{"state": "withdrawn", "version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1")}).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.Account{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error; e != nil {
		return e
	}
	return db.Exec(`UPDATE public.moderation_tasks t SET status='cancelled',version=t.version+1 FROM public.content_registry c WHERE c.id=t.content_id AND c.author_id=? AND c.kind<>'whisper_capsule' AND t.status IN ('pending','human_required')`, id).Error
}
