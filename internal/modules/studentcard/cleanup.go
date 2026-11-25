package studentcard

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

type cardRemover interface {
	RemoveStudentCard(context.Context, string) error
}

// 清理只领取已移出草稿且未绑定任何历史案件的资产。有效扫描租约持有图片写入权。
func cleanupDue(db *gorm.DB) *gorm.DB {
	return db.Model(&entity.Asset{}).Where(`purpose='student_card' AND student_card_slot='removed'
		AND status<>'deleted' AND (student_card_cleanup_at<=clock_timestamp()
			OR (student_card_cleanup_at IS NULL AND status IN ('pending','active','failed')))
		AND EXISTS (SELECT 1 FROM public.accounts u WHERE u.id=assets.owner_id AND u.lifecycle='active')
		AND NOT EXISTS (SELECT 1 FROM public.identity_reviews r WHERE r.asset_id=assets.id)
		AND NOT EXISTS (SELECT 1 FROM public.work_items w WHERE w.target_id=assets.id
			AND w.kind=? AND w.status='leased' AND w.lease_until>clock_timestamp())`, Route().Kind)
}

func (s *Service) processCleanup(ctx context.Context, requestID string) (out durable.Consumption, err error) {
	start := time.Now()
	remover, ok := s.objects.(cardRemover)
	if !ok {
		return durable.Consumption{Deferred: true}, nil
	}
	bounded, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	var asset entity.Asset
	err = cleanupDue(s.db.WithContext(bounded)).Order("student_card_cleanup_at NULLS FIRST,id").Take(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return durable.Consumption{Deferred: true}, nil
	}
	if err != nil {
		return out, err
	}
	defer func() { s.complete("cleanup", requestID, start, err) }()
	claimed, err := s.claimCleanup(bounded, &asset)
	if err != nil || !claimed {
		return durable.Consumption{Deferred: !claimed}, err
	}
	cancel()

	// 删除私有对象发生在短事务之外。每次领取递增资产版本，迟到结果不能覆盖新的重试。
	external, stop := context.WithDeadline(ctx, asset.StudentCardCleanupAt.Add(-s.config.Command.Timeout))
	var envelope securedata.Envelope
	failure := json.Unmarshal(asset.EncryptedObjectKey, &envelope)
	if failure == nil {
		var key []byte
		key, failure = s.codec.Decrypt(external, securedata.Binding{Purpose: "student_card_key", ObjectID: asset.ID, Version: 1}, envelope)
		if failure == nil {
			failure = remover.RemoveStudentCard(external, string(key))
		}
		clear(key)
	}
	stop()
	return s.finishCleanup(ctx, asset, failure)
}

func cardAssetLock(ctx context.Context, db *gorm.DB, asset entity.Asset, timeout time.Duration) (*command.Tx, error) {
	return command.LockPlan(ctx, db, command.Discovery{
		Accounts:  []string{asset.OwnerID},
		Resources: []command.Resource{{Kind: command.Asset, ID: asset.ID}},
	}, timeout)
}

func (s *Service) claimCleanup(ctx context.Context, asset *entity.Asset) (claimed bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		lock, e := cardAssetLock(ctx, db, *asset, s.config.Command.LockTimeout)
		if e != nil {
			return e
		}
		var current entity.Asset
		e = cleanupDue(db).Where("assets.id=? AND assets.owner_id=?", asset.ID, asset.OwnerID).Take(&current).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if e = db.Model(&entity.WorkItem{}).Where("kind=? AND target_kind=? AND target_id=? AND status NOT IN ('completed','cancelled')", Route().Kind, Route().TargetKind, current.ID).
			Updates(map[string]any{"status": "cancelled", "lease_until": nil, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		until := lock.Now().Add(s.config.Lease)
		if e = db.Model(&entity.Asset{}).Where("id=?", current.ID).Updates(map[string]any{
			"status": "deleting", "student_card_cleanup_at": until, "version": gorm.Expr("version+1"),
		}).Error; e != nil {
			return e
		}
		current.StudentCardCleanupAt = &until
		current.Version++
		*asset = current
		claimed = true
		return nil
	})
	return
}

func (s *Service) finishCleanup(ctx context.Context, asset entity.Asset, failure error) (out durable.Consumption, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		lock, e := cardAssetLock(ctx, db, asset, s.config.Command.LockTimeout)
		if e != nil {
			return e
		}
		current := db.Model(&entity.Asset{}).Where("id=? AND version=? AND status='deleting' AND student_card_slot='removed'", asset.ID, asset.Version)
		if failure != nil {
			return current.Update("student_card_cleanup_at", lock.Now().Add(s.config.RetryDelay)).Error
		}
		result := current.Updates(map[string]any{
			"status": "deleted", "encrypted_object_key": gorm.Expr("'{}'::jsonb"), "object_version": "deleted",
			"student_card_cleanup_at": nil, "version": gorm.Expr("version+1"),
		})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if e = db.Where("asset_id=?", asset.ID).Delete(&entity.StudentCardPayload{}).Error; e != nil {
			return e
		}
		out.Applied = true
		return nil
	})
	if err == nil && failure != nil {
		err = command.ErrUnavailable
	}
	return
}
