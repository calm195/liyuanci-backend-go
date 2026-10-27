package wish

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// StoryHolds 将故事与父心愿屏障分开比较，避免两种独立代次相互抵消。
type StoryHolds struct {
	Epoch, ParentEpoch, RequiredVersion int64
	Active, ParentActive                bool
}

// ReadStoryHolds 在调用方快照内读取稳定内容标识上的全部屏障。
func ReadStoryHolds(ctx context.Context, db *gorm.DB, id string) (out StoryHolds, err error) {
	var rows []entity.PrivacyHold
	err = db.WithContext(ctx).Where("(target_kind='story' OR target_kind='wish') AND target_id=?", id).Find(&rows).Error
	if err != nil {
		return
	}
	for _, h := range rows {
		if h.TargetKind == "wish" {
			if h.HoldEpoch > out.ParentEpoch {
				out.ParentEpoch = h.HoldEpoch
			}
			out.ParentActive = out.ParentActive || h.Status == "active"
		} else {
			if h.HoldEpoch > out.Epoch {
				out.Epoch = h.HoldEpoch
			}
			if h.RequiredContentVersion > out.RequiredVersion {
				out.RequiredVersion = h.RequiredContentVersion
			}
			out.Active = out.Active || h.Status == "active"
		}
	}
	return
}

// DiscoverStory 以心愿聚合作为故事命令的版本和锁边界，补齐历史任务及原参与者。
func DiscoverStory(ctx context.Context, db *gorm.DB, id string) (plan command.Discovery, err error) {
	plan, err = DiscoverInteraction(ctx, db, id)
	if err != nil {
		return
	}
	db = db.WithContext(ctx)
	var rows []entity.StoryVersion
	if err = db.Where("wish_id=?", id).Find(&rows).Error; err != nil {
		return
	}
	for _, v := range rows {
		if v.ReviewTaskID != nil {
			plan.Resources = append(plan.Resources, command.Resource{Kind: command.ModerationTask, ID: *v.ReviewTaskID})
		}
		if v.SourceMatchID != nil {
			var matches []entity.Match
			if err = db.Where("id=?", *v.SourceMatchID).Find(&matches).Error; err != nil {
				return
			}
			for _, m := range matches {
				plan.Accounts = append(plan.Accounts, m.RequesterID, m.HelperID)
				plan.Resources = append(plan.Resources, command.Resource{Kind: command.Match, ID: m.ID})
			}
		}
	}
	return
}

// StoryEligible 在统一锁下复核故事精确任务及当前权限；内容审核不改变fulfilled状态。
func StoryEligible(ctx context.Context, db *gorm.DB, w entity.Wish, v entity.StoryVersion, t entity.ModerationTask) (bool, error) {
	if w.DraftDeletedAt != nil || w.State != "fulfilled" || w.CurrentStoryVersion != v.StoryVersion || v.WithdrawnAt != nil || v.ReviewTaskID == nil || *v.ReviewTaskID != t.ID || v.AuthorID != w.OwnerID || t.ContentVersion != v.StoryVersion || t.RestrictionEpoch == nil || *t.RestrictionEpoch != w.RestrictionEpoch || t.Status == "obsolete" || t.Status == "cancelled" {
		return false, nil
	}
	db = db.WithContext(ctx)
	var a entity.Account
	if e := db.Where("id=?", w.OwnerID).Take(&a).Error; e != nil {
		return false, e
	}
	if a.Lifecycle != "active" || a.Verification != "verified" || a.AuthEpoch != t.SubjectEpoch {
		return false, nil
	}
	var n int64
	if e := db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ? AND revoked_at IS NULL AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp())", a.ID, []string{"mute", "ban"}).Count(&n).Error; e != nil {
		return false, e
	}
	if n > 0 {
		return false, nil
	}
	if e := db.Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.entity_id=? AND c.kind IN ('wish','story') AND r.active", w.ID).Count(&n).Error; e != nil {
		return false, e
	}
	if n > 0 {
		return false, nil
	}
	holds, e := ReadStoryHolds(ctx, db, w.ID)
	if e != nil {
		return false, e
	}
	if t.HoldEpoch != holds.Epoch || v.ParentHoldEpoch != holds.ParentEpoch || holds.ParentActive || holds.Active && v.StoryVersion < holds.RequiredVersion {
		return false, nil
	}
	if v.SourceMatchID != nil {
		var matches []entity.Match
		if e = db.Where("id=? AND wish_id=? AND requester_id=?", *v.SourceMatchID, w.ID, w.OwnerID).Find(&matches).Error; e != nil {
			return false, e
		}
		if len(matches) != 1 || v.PeerEpoch == nil {
			return false, nil
		}
		var peers []entity.Account
		if e = db.Where("id=?", matches[0].HelperID).Find(&peers).Error; e != nil {
			return false, e
		}
		if len(peers) != 1 || peers[0].Lifecycle != "active" || peers[0].AuthEpoch != *v.PeerEpoch {
			return false, nil
		}
	} else if holds.Epoch == 0 {
		return false, nil
	}
	return true, nil
}

// DecryptStory 只供扫描或已授权内部处理读取原稿，不开放历史原稿查询。
func DecryptStory(ctx context.Context, codec *securedata.Codec, v entity.StoryVersion) (string, error) {
	var envelope securedata.Envelope
	if json.Unmarshal(v.BodyCipher, &envelope) != nil {
		return "", command.ErrUnavailable
	}
	plain, e := codec.Decrypt(ctx, securedata.Binding{Purpose: "story_content", ObjectID: v.WishID, Version: uint64(v.StoryVersion)}, envelope)
	defer clear(plain)
	var body string
	if e != nil || json.Unmarshal(plain, &body) != nil {
		return "", command.ErrUnavailable
	}
	return body, nil
}

func storyRow(ctx context.Context, db *gorm.DB, id string, version int64) (out entity.StoryVersion, err error) {
	err = db.WithContext(ctx).Where("wish_id=? AND story_version=?", id, version).Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

func invalidateStories(ctx context.Context, tx *command.Tx, id string) error {
	db := tx.DB().WithContext(ctx)
	var tasks []string
	if e := db.Model(&entity.StoryVersion{}).Where("wish_id=? AND review_task_id IS NOT NULL", id).Pluck("review_task_id", &tasks).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.ModerationTask{}).Where("id IN ?", tasks).Updates(map[string]any{"status": "obsolete", "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.StoryVersion{}).Where("wish_id=?", id).Updates(map[string]any{"moderation_status": "obsolete", "public_body": nil}).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.ContentRegistry{}).Where("kind='story' AND entity_id=?", id).Update("status", "obsolete").Error; e != nil {
		return e
	}
	return obsoleteWork(ctx, tx, "moderation_task", tasks)
}

// ApplyStoryReview 写入精确故事投影并释放其已覆盖屏障，不解除父心愿屏障或恢复旧版本。
func ApplyStoryReview(ctx context.Context, db *gorm.DB, w entity.Wish, v entity.StoryVersion, t entity.ModerationTask, decision string, body *string, tags []byte) error {
	db = db.WithContext(ctx)
	updates := map[string]any{"moderation_status": decision, "public_body": body, "identity_tags": json.RawMessage(tags), "published_at": nil}
	if decision == "approved" {
		// 同版本幂等复核保留首次公开位置；新版本在批准事务内建立独立位置。
		updates["published_at"] = gorm.Expr("COALESCE(published_at, clock_timestamp())")
	}
	if e := db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", w.ID, v.StoryVersion).Updates(updates).Error; e != nil {
		return e
	}
	if decision == "approved" {
		if e := db.Model(&entity.PrivacyHold{}).Where("target_kind='story' AND target_id=? AND status='active' AND hold_epoch<=? AND required_content_version<=?", w.ID, t.HoldEpoch, v.StoryVersion).Updates(map[string]any{"status": "released", "released_at": gorm.Expr("clock_timestamp()")}).Error; e != nil {
			return e
		}
	}
	return db.Model(&entity.Wish{}).Where("id=?", w.ID).Update("version", gorm.Expr("version+1")).Error
}
