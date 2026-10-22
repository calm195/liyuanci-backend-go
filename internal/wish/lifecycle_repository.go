package wish

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

type aggregate struct {
	row      entity.Wish
	content  entity.WishVersion
	snapshot Snapshot
	owner    Eligibility
	account  entity.Account
	registry entity.ContentRegistry
	task     *entity.ModerationTask
}

func readAggregate(ctx context.Context, db *gorm.DB, id string, includeDeleted bool) (out aggregate, err error) {
	db = db.WithContext(ctx)
	q := db.Where("id=?", id)
	if !includeDeleted {
		q = q.Where("draft_deleted_at IS NULL")
	}
	if err = q.Take(&out.row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return out, command.ErrNotFound
	} else if err != nil {
		return out, err
	}
	if err = db.Where("id=?", out.row.OwnerID).Take(&out.account).Error; err != nil {
		return out, err
	}
	if err = db.Where("wish_id=? AND content_version=?", id, out.row.CurrentContentVersion).Take(&out.content).Error; err != nil {
		return out, err
	}
	if err = db.Where("kind='wish' AND entity_id=? AND content_version=? AND author_id=?", id, out.row.CurrentContentVersion, out.row.OwnerID).Take(&out.registry).Error; err != nil {
		return out, err
	}
	out.snapshot = Snapshot{State: State(out.row.State), Version: out.row.Version, ContentVersion: out.row.CurrentContentVersion, DurationDays: int(out.row.DurationDays), ExpiresAt: out.row.ExpiresAt, PublishedAt: out.row.PublishedAt, RefreshOnPublish: out.row.RefreshOnPublish, RestrictionEpoch: out.row.RestrictionEpoch}
	var penalties int64
	if err = db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ? AND revoked_at IS NULL AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp())", out.row.OwnerID, []string{"mute", "ban"}).Count(&penalties).Error; err != nil {
		return out, err
	}
	out.owner = Eligibility{Active: out.account.Lifecycle == "active", Verified: out.account.Verification == "verified", Restricted: penalties > 0, HideIdentityTags: out.account.HideIdentityTags}
	// 检查全部历史精确版本上的有效限制，不能通过编辑生成新注册记录绕过旧限制。
	var restrictions int64
	if err = db.Model(&entity.ContentRestriction{}).Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind='wish' AND c.entity_id=? AND r.active", id).Count(&restrictions).Error; err != nil {
		return out, err
	}
	out.snapshot.Restricted = restrictions > 0
	var holds []entity.PrivacyHold
	if err = db.Where("target_kind='wish' AND target_id=?", id).Find(&holds).Error; err != nil {
		return out, err
	}
	for _, h := range holds {
		if h.HoldEpoch > out.snapshot.HoldEpoch {
			out.snapshot.HoldEpoch = h.HoldEpoch
		}
		out.snapshot.PrivacyHeld = out.snapshot.PrivacyHeld || h.Status == "active"
	}
	if out.content.ReviewTaskID != nil {
		var task entity.ModerationTask
		if err = db.Where("id=? AND content_id=? AND content_version=?", *out.content.ReviewTaskID, out.registry.ID, out.content.ContentVersion).Take(&task).Error; err != nil {
			return out, err
		}
		out.task = &task
		r := Review{ContentVersion: task.ContentVersion, HoldEpoch: task.HoldEpoch, Approved: task.Status == "approved" && task.Decision != nil && *task.Decision == "approved" && task.SubjectEpoch == out.account.AuthEpoch && out.registry.Status == "approved" && out.content.ModerationStatus == "approved", DetectionCompleted: out.content.SchoolDetectionState == "completed" && out.registry.SchoolDetectionState == "completed", SchoolRelated: out.content.SchoolRelated, HumanReviewed: task.ReviewerID != nil}
		if task.RestrictionEpoch != nil {
			r.RestrictionEpoch = *task.RestrictionEpoch
		}
		if out.content.PublicBody != nil {
			r.PublicBody = *out.content.PublicBody
		}
		if err = json.Unmarshal(out.content.IdentityTags, &r.Tags); err != nil {
			return out, err
		}
		// 注册表和内容检测必须一致，检测器占位标识绝不视为已完成检测。
		if out.content.SchoolDetectionVersion == "pending" || out.registry.SchoolDetectionVersion != out.content.SchoolDetectionVersion || out.registry.SchoolRelated == nil || r.SchoolRelated == nil || *out.registry.SchoolRelated != *r.SchoolRelated {
			r.DetectionCompleted = false
		}
		out.snapshot.Review = r
	}
	return out, nil
}

// discoverWish 在主体guard之前只读发现完整参与者；引擎在取锁后重发现，集合变化会回滚重试。
func discoverWish(ctx context.Context, db *gorm.DB, id string) (plan command.Discovery, err error) {
	db = db.WithContext(ctx)
	var row entity.Wish
	if err = db.Where("id=?", id).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return plan, command.ErrNotFound
	} else if err != nil {
		return plan, err
	}
	plan.Accounts = []string{row.OwnerID}
	plan.Resources = []command.Resource{{Kind: command.Wish, ID: id}}
	var apps []entity.Application
	if err = db.Where("wish_id=? AND status IN ?", id, []string{"pending_scan", "pending_review", "active"}).Order("id").Find(&apps).Error; err != nil {
		return plan, err
	}
	for _, a := range apps {
		plan.Accounts = append(plan.Accounts, a.ApplicantID)
		plan.Resources = append(plan.Resources, command.Resource{Kind: command.Application, ID: a.ID})
	}
	var tasks []entity.ModerationTask
	if err = db.Model(&entity.ModerationTask{}).Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.kind='wish' AND c.entity_id=?", id).Order("t.id").Find(&tasks).Error; err != nil {
		return plan, err
	}
	for _, task := range tasks {
		plan.Resources = append(plan.Resources, command.Resource{Kind: command.ModerationTask, ID: task.ID})
	}
	return plan, nil
}

func saveState(ctx context.Context, tx *command.Tx, current aggregate, next Snapshot) error {
	result := tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where("id=? AND version=? AND draft_deleted_at IS NULL", current.row.ID, current.row.Version).Updates(map[string]any{"state": next.State, "version": next.Version, "current_content_version": next.ContentVersion, "duration_days": next.DurationDays, "refresh_on_publish": next.RefreshOnPublish, "expires_at": next.ExpiresAt, "published_at": next.PublishedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return command.ErrVersionConflict
	}
	return nil
}

// obsoleteWork 在账号/资源锁之后按工作ID锁定；与消费者先业务锁后工作源的顺序一致。
func obsoleteWork(ctx context.Context, tx *command.Tx, targetKind string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var rows []entity.WorkItem
	db := tx.DB().WithContext(ctx)
	if err := db.Where("target_kind=? AND target_id IN ?", targetKind, ids).Order("id").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if !slices.Contains([]string{"completed", "cancelled"}, row.Status) {
			if err := db.Model(&entity.WorkItem{}).Where("id=?", row.ID).Updates(map[string]any{"status": "cancelled", "lease_until": nil, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func invalidate(ctx context.Context, tx *command.Tx, id string, applications bool, reason string) error {
	db := tx.DB().WithContext(ctx)
	var registryIDs []string
	if err := db.Model(&entity.ContentRegistry{}).Where("kind='wish' AND entity_id=?", id).Pluck("id", &registryIDs).Error; err != nil {
		return err
	}
	var tasks []entity.ModerationTask
	if err := db.Where("content_id IN ?", registryIDs).Order("id").Find(&tasks).Error; err != nil {
		return err
	}
	taskIDs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		taskIDs = append(taskIDs, t.ID)
	}
	if err := db.Model(&entity.ModerationTask{}).Where("id IN ?", taskIDs).Update("status", "obsolete").Error; err != nil {
		return err
	}
	if err := db.Model(&entity.ContentRegistry{}).Where("id IN ?", registryIDs).Update("status", "obsolete").Error; err != nil {
		return err
	}
	if err := db.Model(&entity.WishVersion{}).Where("wish_id=?", id).Updates(map[string]any{"moderation_status": "obsolete", "public_body": nil}).Error; err != nil {
		return err
	}
	if err := obsoleteWork(ctx, tx, "moderation_task", taskIDs); err != nil {
		return err
	}
	if applications {
		var apps []entity.Application
		if err := db.Where("wish_id=? AND status IN ?", id, []string{"pending_scan", "pending_review", "active"}).Order("id").Find(&apps).Error; err != nil {
			return err
		}
		var appIDs []string
		for _, a := range apps {
			appIDs = append(appIDs, a.ID)
		}
		if err := db.Model(&entity.Application{}).Where("id IN ?", appIDs).Updates(map[string]any{"status": "invalidated", "cancel_reason": reason, "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
		if err := obsoleteWork(ctx, tx, "application", appIDs); err != nil {
			return err
		}
	}
	return nil
}
