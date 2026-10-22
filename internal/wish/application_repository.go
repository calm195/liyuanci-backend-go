package wish

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// DiscoverInteraction 只读发现心愿、有效申请、当前匹配/会话及待审消息的完整锁集合。
// 所有参与账号先于业务资源锁定；调用方须在取锁后重新发现，不在事务中临时追加锁。
func DiscoverInteraction(ctx context.Context, db *gorm.DB, id string) (plan command.Discovery, err error) {
	plan, err = discoverWishBase(ctx, db, id)
	if err != nil {
		return plan, err
	}
	db = db.WithContext(ctx)
	var matches []entity.Match
	if err = db.Where("wish_id=? AND status='current'", id).Find(&matches).Error; err != nil {
		return plan, err
	}
	for _, m := range matches {
		plan.Accounts = append(plan.Accounts, m.RequesterID, m.HelperID)
		plan.Resources = append(plan.Resources, command.Resource{Kind: command.Match, ID: m.ID})
		var conversations []entity.Conversation
		if err = db.Where("match_id=?", m.ID).Find(&conversations).Error; err != nil {
			return plan, err
		}
		for _, c := range conversations {
			plan.Resources = append(plan.Resources, command.Resource{Kind: command.Conversation, ID: c.ID})
			var messages []entity.Message
			if err = db.Where("conversation_id=? AND status IN ?", c.ID, []string{"pending_scan", "pending_review"}).Find(&messages).Error; err != nil {
				return plan, err
			}
			for _, msg := range messages {
				if msg.ReviewTaskID != nil {
					plan.Resources = append(plan.Resources, command.Resource{Kind: command.ModerationTask, ID: *msg.ReviewTaskID})
				}
				plan.Resources = append(plan.Resources, command.Resource{Kind: command.Message, ID: msg.ID})
			}
		}
	}
	return plan, nil
}

func readApplication(ctx context.Context, db *gorm.DB, id string) (out entity.Application, err error) {
	err = db.WithContext(ctx).Where("id=?", id).Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

func applicationPlan(ctx context.Context, db *gorm.DB, id string) (plan command.Discovery, err error) {
	row, err := readApplication(ctx, db, id)
	if err != nil {
		return plan, err
	}
	plan, err = DiscoverInteraction(ctx, db, row.WishID)
	if err != nil {
		return plan, err
	}
	plan.Accounts = append(plan.Accounts, row.ApplicantID)
	plan.Resources = append(plan.Resources, command.Resource{Kind: command.Application, ID: row.ID})
	if row.ReviewTaskID != nil {
		plan.Resources = append(plan.Resources, command.Resource{Kind: command.ModerationTask, ID: *row.ReviewTaskID})
	}
	return
}

func participantAllowed(ctx context.Context, db *gorm.DB, id string, now time.Time) (bool, error) {
	var n int64
	if err := db.WithContext(ctx).Model(&entity.Account{}).Where("id=? AND lifecycle='active' AND verification='verified'", id).Count(&n).Error; err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	err := db.WithContext(ctx).Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ? AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", id, []string{"mute", "ban"}, now, now).Count(&n).Error
	return n == 0, err
}

func blocked(ctx context.Context, db *gorm.DB, a, b string) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Model(&entity.Block{}).Where("(owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?)", a, b, b, a).Count(&n).Error
	return n > 0, err
}

func applicationTarget(ctx context.Context, db *gorm.DB, a aggregate, applicant string, now time.Time) error {
	if a.row.OwnerID == applicant {
		return command.ErrForbidden
	}
	if !ordinary(a.owner, a.snapshot) {
		return command.ErrForbidden
	}
	if a.row.State != string(Published) || a.row.ExpiresAt == nil || !now.Before(*a.row.ExpiresAt) || !reviewReady(a.snapshot) {
		return command.ErrConflict
	}
	ok, err := participantAllowed(ctx, db, applicant, now)
	if err != nil {
		return err
	}
	if !ok {
		return command.ErrForbidden
	}
	denied, err := blocked(ctx, db, applicant, a.row.OwnerID)
	if err != nil {
		return err
	}
	if denied {
		return command.ErrBlockedInteraction
	}
	var n int64
	err = db.WithContext(ctx).Model(&entity.Match{}).Where("wish_id=? AND status='current'", a.row.ID).Count(&n).Error
	if err != nil {
		return err
	}
	if n != 0 {
		return command.ErrConflict
	}
	return nil
}

// ApplicationEligible 复核精确申请与当前心愿/双方账号状态，不把扫描或曾经有效当作授权。
// 账号、心愿、申请及任务必须由写入调用方按统一锁序持有；只读列表可在一致快照调用。
func ApplicationEligible(ctx context.Context, db *gorm.DB, row entity.Application, now time.Time) (bool, error) {
	if row.Status != "pending_scan" && row.Status != "pending_review" && row.Status != "active" {
		return false, nil
	}
	if row.OwnerEpoch == nil || row.ReviewTaskID == nil {
		return false, nil
	}
	a, err := readAggregate(ctx, db, row.WishID, false)
	if errors.Is(err, command.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.WishContentVersion != a.row.CurrentContentVersion || *row.OwnerEpoch != a.account.AuthEpoch {
		return false, nil
	}
	err = applicationTarget(ctx, db, a, row.ApplicantID, now)
	if errors.Is(err, command.ErrForbidden) || errors.Is(err, command.ErrConflict) || errors.Is(err, command.ErrBlockedInteraction) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var restrictions int64
	if err = db.WithContext(ctx).Model(&entity.ContentRestriction{}).Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind='application' AND c.entity_id=? AND r.active", row.ID).Count(&restrictions).Error; err != nil {
		return false, err
	}
	if restrictions > 0 {
		return false, nil
	}
	var task entity.ModerationTask
	if err = db.WithContext(ctx).Where("id=?", *row.ReviewTaskID).Take(&task).Error; err != nil {
		return false, err
	}
	var actor entity.Account
	if err = db.WithContext(ctx).Where("id=?", row.ApplicantID).Take(&actor).Error; err != nil {
		return false, err
	}
	if row.Status == "active" {
		if task.Status != "approved" || task.Decision == nil || *task.Decision != "approved" {
			return false, nil
		}
		var registry entity.ContentRegistry
		if err = db.WithContext(ctx).Where("id=? AND kind='application' AND entity_id=? AND content_version=?", task.ContentID, row.ID, row.ContentVersion).Take(&registry).Error; err != nil {
			return false, err
		}
		if registry.Status != "approved" {
			return false, nil
		}
	}
	return task.SubjectEpoch == actor.AuthEpoch && task.ContentVersion == row.ContentVersion && task.RestrictionEpoch != nil && *task.RestrictionEpoch == a.row.RestrictionEpoch && task.HoldEpoch == a.snapshot.HoldEpoch && task.Status != "obsolete" && task.Status != "cancelled", nil
}

// CancelApplication 在已锁申请及案件上终结旧交互；不会撤销已建立匹配，调用方另行处理匹配状态。
// 工作源由持久消费者自己终结，不能在其事务中倒序取得其他工作锁。
func CancelApplication(ctx context.Context, db *gorm.DB, row entity.Application, status, reason string) error {
	if err := db.WithContext(ctx).Model(&entity.Application{}).Where("id=?", row.ID).Updates(map[string]any{"status": status, "cancel_reason": reason, "public_message_cipher": nil, "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	if row.ReviewTaskID != nil {
		if err := db.WithContext(ctx).Model(&entity.ModerationTask{}).Where("id=?", *row.ReviewTaskID).Updates(map[string]any{"status": "obsolete", "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
	}
	return nil
}

func applicationCipher(ctx context.Context, codec *securedata.Codec, id, purpose string, version int64, message string) (json.RawMessage, error) {
	plain, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	envelope, err := codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, plain)
	if err != nil {
		return nil, command.ErrUnavailable
	}
	return json.Marshal(envelope)
}

func applicationMessage(ctx context.Context, codec *securedata.Codec, row entity.Application) (string, error) {
	var envelope securedata.Envelope
	if json.Unmarshal(row.PublicMessageCipher, &envelope) != nil {
		return "", command.ErrUnavailable
	}
	data, err := codec.Decrypt(ctx, securedata.Binding{Purpose: "application_approved", ObjectID: row.ID, Version: uint64(row.ContentVersion)}, envelope)
	if err != nil {
		return "", command.ErrUnavailable
	}
	defer clear(data)
	var message string
	if json.Unmarshal(data, &message) != nil || !validApplicationMessage(message) {
		return "", command.ErrUnavailable
	}
	return message, nil
}

func notify(ctx context.Context, db *gorm.DB, recipient, kind, target string) error {
	return db.WithContext(ctx).Create(&entity.Notification{ID: uuid.NewString(), RecipientID: recipient, Type: kind, TargetRef: target}).Error
}
