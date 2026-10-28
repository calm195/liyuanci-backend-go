package whisper

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// CapsuleUnlockRoute 固定预约工作和技术唤醒，不携带正文或账号。
func CapsuleUnlockRoute() durable.Route {
	return durable.Route{Kind: "whisper_capsule_unlock", TargetKind: "whisper_capsule", EventType: "whisper.capsule.unlock.requested"}
}

func (s *Service) scheduleCapsule(ctx context.Context, db *gorm.DB, id string, at time.Time) error {
	work := uuid.NewString()
	if e := durable.Append(ctx, db, s.logger, CapsuleUnlockRoute(), durable.Draft{WorkID: work, OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: id, AggregateVersion: 1}); e != nil {
		return e
	}
	if e := db.WithContext(ctx).Model(&entity.WorkItem{}).Where("id=?", work).Update("next_at", at).Error; e != nil {
		return e
	}
	return db.WithContext(ctx).Model(&entity.OutboxEvent{}).Where("work_id=?", work).Update("next_at", at).Error
}

// ResumeCapsuleUnlock 在审核批准事务内唤醒已挂起的工作；不改动完成、取消或异常待办。
// 调用者须持本人账号guard与胶囊资源锁，所有工作锁均在业务资源之后。
func ResumeCapsuleUnlock(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Exec(`UPDATE public.work_items SET status='pending',version=version+1,next_at=GREATEST((SELECT unlock_at FROM public.whisper_capsules WHERE id=?),clock_timestamp()) WHERE kind='whisper_capsule_unlock' AND target_id=? AND status='waiting'`, id, id).Error
}

func cancelCapsuleWork(ctx context.Context, db *gorm.DB, c entity.WhisperCapsule, now time.Time) error {
	db = db.WithContext(ctx)
	if e := db.Exec(`UPDATE public.work_items SET status='cancelled',version=version+1,lease_until=NULL WHERE status<>'cancelled' AND ((kind='whisper_capsule_unlock' AND target_id=?) OR (kind='whisper_review' AND target_id IN (SELECT id FROM public.moderation_tasks WHERE content_id=?)))`, c.ID, c.ContentRegistryID).Error; e != nil {
		return e
	}
	if e := db.Exec(`UPDATE public.outbox_events SET superseded_at=?,lease_until=NULL WHERE superseded_at IS NULL AND work_id IN (SELECT id FROM public.work_items WHERE (kind='whisper_capsule_unlock' AND target_id=?) OR (kind='whisper_review' AND target_id IN (SELECT id FROM public.moderation_tasks WHERE content_id=?)))`, now, c.ID, c.ContentRegistryID).Error; e != nil {
		return e
	}
	// 已送达通知亦只属普通副本，取消后不留可点击的旧胶囊提醒；限定危机证据不在此图中。
	var ids []string
	if e := db.Model(&entity.WhisperNotificationRef{}).Where("kind='whisper_capsule_unlocked' AND source_id=?", c.ID).Pluck("notification_id", &ids).Error; e != nil {
		return e
	}
	if len(ids) == 0 {
		return nil
	}
	if e := db.Where("notification_id IN ?", ids).Delete(&entity.DeliveryAttempt{}).Error; e != nil {
		return e
	}
	if e := db.Where("notification_id IN ?", ids).Delete(&entity.WhisperNotificationRef{}).Error; e != nil {
		return e
	}
	return db.Where("id IN ?", ids).Delete(&entity.Notification{}).Error
}

// CapsuleUnlockWorker 在持久消费事务内只建立本人站内通知，不解密或外发正文。
type CapsuleUnlockWorker struct {
	db       *gorm.DB
	logger   *zap.Logger
	consumer *durable.Consumer
	timeout  time.Duration
}

// NewCapsuleUnlockWorker 不迁移或启动循环；持久挂起和补偿由宿主显式调度。
func NewCapsuleUnlockWorker(db *gorm.DB, logger *zap.Logger, cfg durable.ConsumerConfig) (*CapsuleUnlockWorker, error) {
	def := durable.ConsumerDefinition{ConsumerKind: "whisper.capsule.unlock", Route: CapsuleUnlockRoute()}
	def.Discover = func(ctx context.Context, db *gorm.DB, work durable.Work) (command.Discovery, error) {
		var rows []entity.WhisperCapsule
		if e := db.WithContext(ctx).Select("id,owner_id").Where("id=?", work.TargetID).Find(&rows).Error; e != nil {
			return command.Discovery{}, e
		}
		if len(rows) == 0 {
			return command.Discovery{}, nil
		}
		return command.Discovery{Accounts: []string{rows[0].OwnerID}, Resources: []command.Resource{{Kind: command.WhisperCapsule, ID: work.TargetID}}}, nil
	}
	def.Apply = func(ctx context.Context, tx *command.Tx, work durable.Work) (durable.Decision, error) {
		db := tx.DB().WithContext(ctx)
		var c entity.WhisperCapsule
		// 解锁只需元数据，领域worker的正文读取权限不在本工作使用。
		e := db.Select("id,owner_id,content_registry_id,lifecycle,review_state,unlock_at").Where("id=?", work.TargetID).Take(&c).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return durable.Obsolete, nil
		}
		if e != nil {
			return "", e
		}
		guard, ok := tx.Guard(c.OwnerID)
		if !ok {
			return "", command.ErrInternal
		}
		if guard.Lifecycle != "active" || c.Lifecycle != "active" || work.TargetVersion != 1 {
			return durable.Obsolete, nil
		}
		flags, e := capsuleBarriers(ctx, db, c.ID, c.ContentRegistryID)
		if e != nil {
			return "", e
		}
		if c.UnlockAt.After(tx.Now()) || capsuleStatus(c, flags, tx.Now()) != "unlocked" {
			return durable.Suspend, nil
		}
		var count int64
		if e = db.Model(&entity.WhisperNotificationRef{}).Where("kind='whisper_capsule_unlocked' AND source_id=?", c.ID).Count(&count).Error; e != nil {
			return "", e
		}
		if count > 0 {
			return durable.Complete, nil
		}
		id := uuid.NewString()
		if e = db.Create(&entity.Notification{ID: id, RecipientID: c.OwnerID, Type: "whisper_capsule_unlocked", TargetRef: c.ID, CreatedAt: tx.Now()}).Error; e != nil {
			return "", e
		}
		e = db.Create(&entity.WhisperNotificationRef{NotificationID: id, Kind: "whisper_capsule_unlocked", SourceID: c.ID, TargetKind: "capsule", TargetID: c.ID}).Error
		return durable.Complete, e
	}
	c, e := durable.NewConsumer(db, logger, def, cfg)
	if e != nil {
		return nil, e
	}
	return &CapsuleUnlockWorker{db: db, logger: logger, consumer: c, timeout: cfg.Timeout}, nil
}

// Consume 复用工作代次、租约fencing和完成回执；提前唤醒不会改变可读时间。
func (w *CapsuleUnlockWorker) Consume(ctx context.Context, event durable.Event) (durable.Consumption, error) {
	return w.consumer.Consume(ctx, event)
}

// ProcessNext 有界补偿丢失唤醒和批准事件；未批准的waiting不再被轮询或消耗重试次数。
func (w *CapsuleUnlockWorker) ProcessNext(ctx context.Context) (out durable.Consumption, err error) {
	start := time.Now()
	defer func() {
		w.logger.Info("胶囊解锁补偿完成", zap.Bool("success", err == nil), zap.Bool("applied", out.Applied), zap.Duration("duration", time.Since(start)))
	}()
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	var events []durable.Event
	err = w.db.WithContext(ctx).Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.work_items w JOIN public.outbox_events e ON e.work_id=w.id AND e.superseded_at IS NULL LEFT JOIN public.whisper_capsules c ON c.id=w.target_id WHERE w.kind='whisper_capsule_unlock' AND w.next_at<=clock_timestamp() AND (w.status IN ('pending','retry_wait') OR (w.status='leased' AND w.lease_until<=clock_timestamp()) OR (w.status='waiting' AND c.lifecycle='active' AND c.review_state='approved' AND c.unlock_at<=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind='whisper_capsule' AND h.target_id=c.id AND h.status='active') AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=c.content_registry_id AND x.active))) ORDER BY w.next_at,w.id LIMIT 1`).Scan(&events).Error
	if err != nil {
		return out, durable.ErrUnavailable
	}
	if len(events) == 0 {
		return durable.Consumption{Deferred: true}, nil
	}
	return w.Consume(ctx, events[0])
}
