package studentcard

import (
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

type leased struct {
	work    entity.WorkItem
	asset   entity.Asset
	payload entity.StudentCardPayload
}

// ProcessNext 只从数据库扫描一条到期工作；MQ丢失唤醒时仍可由显式调度补偿。
func (s *Service) ProcessNext(ctx context.Context, requestID string) (out durable.Consumption, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		s.complete("scan_next", requestID, start, err)
	}()
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	var rows []entity.WorkItem
	e := s.db.WithContext(ctx).Where("kind=? AND target_kind=? AND status IN ? AND next_at<=clock_timestamp() AND (status<>? OR lease_until<=clock_timestamp())", Route().Kind, Route().TargetKind, []string{"pending", "retry_wait", "leased"}, "leased").Order("next_at,id").Limit(1).Find(&rows).Error
	if e != nil {
		return out, e
	}
	if len(rows) == 0 {
		out.Deferred = true
		return out, nil
	}
	return s.Process(ctx, rows[0].ID, requestID)
}

// Consume 校验数据库中的当前事件关联后消费；消息只唤醒已有工作，不能重建学生卡。
func (s *Service) Consume(ctx context.Context, event durable.Event) (out durable.Consumption, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		s.complete("scan_event", "", start, err)
	}()
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	if !validID(event.EventID) || !validID(event.CorrelationID) || !validID(event.AggregateID) || event.AggregateVersion != 1 || event.EventType != Route().EventType || event.CreatedAt.IsZero() {
		return out, command.ErrInvalid
	}
	var count int64
	e := s.db.WithContext(ctx).Model(&entity.OutboxEvent{}).Where("event_id=? AND work_id=? AND aggregate_id=? AND aggregate_version=? AND event_type=? AND created_at=? AND superseded_at IS NULL", event.EventID, event.CorrelationID, event.AggregateID, event.AggregateVersion, event.EventType, event.CreatedAt).Count(&count).Error
	if e != nil {
		return out, e
	}
	if count != 1 {
		out.Missing = true
		return out, nil
	}
	work, e := readWork(ctx, s.db, event.CorrelationID, false)
	if e != nil {
		return out, e
	}
	if work.TargetID != event.AggregateID {
		out.Missing = true
		return out, nil
	}
	return s.process(ctx, work.ID, "", &event)
}

// Process 分三段领取、外部调用、最终复核；租约代次阻止旧worker覆盖新结果。
func (s *Service) Process(ctx context.Context, workID, requestID string) (durable.Consumption, error) {
	return s.process(ctx, workID, requestID, nil)
}

func (s *Service) process(ctx context.Context, workID, requestID string, expected *durable.Event) (out durable.Consumption, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = safe(err)
		if err != nil {
			out = durable.Consumption{}
		}
		s.complete("scan", requestID, start, err, zap.Bool("applied", out.Applied), zap.Bool("cancelled", out.Cancelled), zap.Bool("replayed", out.Replayed), zap.Bool("deferred", out.Deferred))
	}()
	if !validID(workID) {
		return out, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	initial, e := readWork(ctx, s.db, workID, false)
	if e != nil {
		return out, e
	}
	if initial.Status == "completed" {
		out.Replayed = true
		return out, nil
	}
	if initial.Status == "cancelled" {
		out.Cancelled = true
		return out, nil
	}
	asset, e := readAsset(ctx, s.db, initial.TargetID)
	if errors.Is(e, command.ErrNotFound) {
		err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			if e := setTimeout(ctx, db, s.config.Command.LockTimeout); e != nil {
				return e
			}
			current, e := readWork(ctx, db, workID, true)
			if e != nil {
				return e
			}
			var count int64
			if e = db.Model(&entity.Asset{}).Where("id=?", current.TargetID).Count(&count).Error; e != nil {
				return e
			}
			if count != 0 {
				return command.ErrConflict
			}
			if current.Status == "completed" {
				out.Replayed = true
				return nil
			}
			if current.Status == "cancelled" {
				out.Cancelled = true
				return nil
			}
			out.Cancelled = true
			return finishWork(ctx, db, current, "cancelled")
		})
		return out, err
	}
	if e != nil {
		return out, e
	}
	var lease leased
	claimed := false
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		tx, e := command.LockPlan(ctx, db, command.Discovery{Accounts: []string{asset.OwnerID}, Resources: []command.Resource{{Kind: command.Asset, ID: asset.ID}}}, s.config.Command.LockTimeout)
		if e != nil {
			return e
		}
		current, e := readWork(ctx, db, workID, true)
		if e != nil {
			return e
		}
		at, e := now(ctx, db)
		if e != nil {
			return e
		}
		if current.TargetID != asset.ID {
			return command.ErrConflict
		}
		// 事件有效性在工作行锁后复核，防止重驱动替换与消息初读之间的窗口。
		if expected != nil {
			var count int64
			if e = db.Model(&entity.OutboxEvent{}).Where("event_id=? AND work_id=? AND aggregate_id=? AND aggregate_version=? AND event_type=? AND created_at=? AND superseded_at IS NULL", expected.EventID, current.ID, current.TargetID, expected.AggregateVersion, expected.EventType, expected.CreatedAt).Count(&count).Error; e != nil {
				return e
			}
			if count != 1 {
				out.Missing = true
				return nil
			}
		}
		if current.Status == "completed" || current.Status == "cancelled" {
			out.Replayed = current.Status == "completed"
			out.Cancelled = current.Status == "cancelled"
			return nil
		}
		guard, ok := tx.Guard(asset.OwnerID)
		if !ok {
			return command.ErrConflict
		}
		currentAsset, e := readAsset(ctx, db, asset.ID)
		if e != nil {
			return e
		}
		if currentAsset.OwnerID != asset.OwnerID {
			return command.ErrConflict
		}
		if guard.Lifecycle != "active" || currentAsset.Status == "deleting" || currentAsset.Status == "deleted" {
			if currentAsset.Status != "deleting" && currentAsset.Status != "deleted" {
				if e = db.Model(&entity.Asset{}).Where("id=?", currentAsset.ID).Updates(map[string]any{"status": "deleting", "version": gorm.Expr("version+1")}).Error; e != nil {
					return e
				}
			}
			out.Cancelled = true
			return finishWork(ctx, db, current, "cancelled")
		}
		if current.Status == "attention" || current.NextAt.After(at) || (current.Status == "leased" && current.LeaseUntil != nil && current.LeaseUntil.After(at)) {
			out.Deferred = true
			return nil
		}
		if int(current.Attempts) >= s.config.MaxAttempts {
			out.Deferred = true
			return db.Model(&entity.WorkItem{}).Where("id=?", current.ID).Updates(map[string]any{"status": "attention", "lease_until": nil, "version": gorm.Expr("version+1")}).Error
		}
		if e = db.Where("asset_id=?", asset.ID).Take(&lease.payload).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				out.Deferred = true
				return db.Model(&entity.WorkItem{}).Where("id=?", current.ID).Updates(map[string]any{"status": "attention", "lease_until": nil, "version": gorm.Expr("version+1")}).Error
			}
			return e
		}
		until := at.Add(s.config.Lease)
		if e = db.Model(&entity.WorkItem{}).Where("id=?", current.ID).Updates(map[string]any{"status": "leased", "lease_until": until, "version": gorm.Expr("version+1"), "attempts": gorm.Expr("attempts+1")}).Error; e != nil {
			return e
		}
		current.Version++
		current.Attempts++
		current.LeaseUntil = &until
		lease.work = current
		lease.asset = currentAsset
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return out, err
	}
	version, approved, failure := s.external(ctx, lease)
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		tx, e := command.LockPlan(ctx, db, command.Discovery{Accounts: []string{asset.OwnerID}, Resources: []command.Resource{{Kind: command.Asset, ID: asset.ID}}}, s.config.Command.LockTimeout)
		if e != nil {
			return e
		}
		current, e := readWork(ctx, db, workID, true)
		if e != nil {
			return e
		}
		at, e := now(ctx, db)
		if e != nil {
			return e
		}
		// 外部成功不代表当前租约仍有效；旧持有者不能确认重领后的工作或写入成功状态。
		if current.Version != lease.work.Version || current.Status != "leased" || current.LeaseUntil == nil || !current.LeaseUntil.After(at) {
			return command.ErrConflict
		}
		latest, e := readAsset(ctx, db, asset.ID)
		if e != nil {
			return e
		}
		guard, _ := tx.Guard(asset.OwnerID)
		if guard.Lifecycle != "active" || latest.Status == "deleting" || latest.Status == "deleted" {
			fields := map[string]any{"status": "deleting", "version": gorm.Expr("version+1")}
			if version != "" {
				fields["object_version"] = version
			}
			if e = db.Model(&entity.Asset{}).Where("id=?", asset.ID).Updates(fields).Error; e != nil {
				return e
			}
			out.Cancelled = true
			return finishWork(ctx, db, current, "cancelled")
		}
		if latest.OwnerID != asset.OwnerID || latest.Version != lease.asset.Version {
			return command.ErrConflict
		}
		if failure != nil {
			status := "retry_wait"
			if int(current.Attempts) >= s.config.MaxAttempts {
				status = "attention"
			}
			// 外部版本即使扫描失败也保留，后续清理可精确定位私有对象；不提前批准图片。
			if version != "" {
				if e = db.Model(&entity.Asset{}).Where("id=?", asset.ID).Update("object_version", version).Error; e != nil {
					return e
				}
			}
			return db.Model(&entity.WorkItem{}).Where("id=?", current.ID).Updates(map[string]any{"status": status, "lease_until": nil, "version": gorm.Expr("version+1"), "next_at": at.Add(s.config.RetryDelay)}).Error
		}
		status, scan := "active", "approved"
		if !approved {
			status, scan = "failed", "rejected"
		}
		if e = db.Model(&entity.Asset{}).Where("id=?", asset.ID).Updates(map[string]any{"status": status, "scan_status": scan, "object_version": version, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		if e = db.Where("asset_id=?", asset.ID).Delete(&entity.StudentCardPayload{}).Error; e != nil {
			return e
		}
		out.Applied = true
		return finishWork(ctx, db, current, "completed")
	})
	if err == nil && failure != nil {
		return out, command.ErrUnavailable
	}
	return out, err
}

// external 只在事务外执行；任何适配器panic均失败关闭，不能被解释为扫描通过。
func (s *Service) external(ctx context.Context, lease leased) (version string, approved bool, err error) {
	defer func() {
		if recover() != nil {
			err = command.ErrUnavailable
		}
	}()
	var keyEnvelope, payload securedata.Envelope
	if json.Unmarshal(lease.asset.EncryptedObjectKey, &keyEnvelope) != nil || json.Unmarshal(lease.payload.Envelope, &payload) != nil {
		return "", false, command.ErrUnavailable
	}
	key, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "student_card_key", ObjectID: lease.asset.ID, Version: 1}, keyEnvelope)
	defer clear(key)
	if e != nil {
		return "", false, e
	}
	version, e = s.objects.PutOnce(ctx, string(key), lease.payload.Envelope)
	if e != nil || version == "" {
		return version, false, command.ErrUnavailable
	}
	image, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "student_card", ObjectID: lease.asset.ID, Version: 1}, payload)
	defer clear(image)
	if e != nil {
		return version, false, e
	}
	if int64(len(image)) > s.config.MaxBytes {
		return version, false, command.ErrUnavailable
	}
	approved, e = s.scanner.Scan(ctx, image, lease.payload.MIME)
	return version, approved, e
}
