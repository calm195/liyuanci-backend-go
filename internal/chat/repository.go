package chat

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// Snapshot 只包含原匹配参与者，不能用当前新匹配推断旧会话归属。
type Snapshot struct {
	Conversation      entity.Conversation
	Match             entity.Match
	Wish              entity.Wish
	Requester, Helper entity.Account
}

// LoadConversation 在调用方事务内读取完整权限快照，不解密正文。
func LoadConversation(ctx context.Context, db *gorm.DB, id string) (s Snapshot, err error) {
	db = db.WithContext(ctx)
	defer func() {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = command.ErrNotFound
		}
	}()
	if err = db.Where("id=?", id).Take(&s.Conversation).Error; err != nil {
		return
	}
	if err = db.Where("id=?", s.Conversation.MatchID).Take(&s.Match).Error; err != nil {
		return
	}
	if err = db.Where("id=?", s.Match.WishID).Take(&s.Wish).Error; err != nil {
		return
	}
	if err = db.Where("id=?", s.Match.RequesterID).Take(&s.Requester).Error; err != nil {
		return
	}
	err = db.Where("id=?", s.Match.HelperID).Take(&s.Helper).Error
	return
}

// CanRead 在双方仍存续时允许原参与者读取终结会话；注销意图立即阻断读取。
func (s Snapshot) CanRead(actor string) bool {
	return (actor == s.Match.RequesterID || actor == s.Match.HelperID) && s.Requester.Lifecycle == "active" && s.Helper.Lifecycle == "active" && s.Wish.DraftDeletedAt == nil && s.Wish.OwnerID == s.Match.RequesterID
}

// DiscoverConversation 返回统一锁序所需原双方账号及父资源。
func DiscoverConversation(ctx context.Context, db *gorm.DB, id string) (command.Discovery, error) {
	s, e := LoadConversation(ctx, db, id)
	if errors.Is(e, command.ErrNotFound) {
		return command.Discovery{}, nil
	}
	if e != nil {
		return command.Discovery{}, e
	}
	return command.Discovery{Accounts: []string{s.Match.RequesterID, s.Match.HelperID}, Resources: []command.Resource{{Kind: command.Wish, ID: s.Wish.ID}, {Kind: command.Match, ID: s.Match.ID}, {Kind: command.Conversation, ID: id}}}, nil
}
func holdEpoch(ctx context.Context, db *gorm.DB, id string) (int64, error) {
	var epoch int64
	e := db.WithContext(ctx).Model(&entity.PrivacyHold{}).Select("coalesce(max(hold_epoch),0)").Where("target_kind='wish' AND target_id=?", id).Scan(&epoch).Error
	return epoch, e
}

// Writable 在已有锁和当前时间下复核双方资格、处罚、拉黑及父内容屏障。
func Writable(ctx context.Context, db *gorm.DB, s Snapshot, now time.Time) error {
	if !s.CanRead(s.Requester.ID) || s.Match.Status != "current" || s.Wish.State != "matching" || s.Requester.Verification != "verified" || s.Helper.Verification != "verified" {
		return command.ErrForbidden
	}
	db = db.WithContext(ctx)
	var n int64
	if e := db.Model(&entity.Penalty{}).Where("account_id IN ? AND action_kind IN ? AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", []string{s.Requester.ID, s.Helper.ID}, []string{"mute", "ban"}, now, now).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrForbidden
	}
	if e := db.Model(&entity.Block{}).Where("(owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?)", s.Requester.ID, s.Helper.ID, s.Helper.ID, s.Requester.ID).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrBlockedInteraction
	}
	if e := db.Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind='wish' AND c.entity_id=? AND r.active", s.Wish.ID).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrForbidden
	}
	if e := db.Model(&entity.PrivacyHold{}).Where("target_kind='wish' AND target_id=? AND status='active'", s.Wish.ID).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrForbidden
	}
	return nil
}

// EligibleMessage 复核提交时双方代次及全部屏障；旧任务不能因权限恢复而重新生效。
func EligibleMessage(ctx context.Context, db *gorm.DB, m entity.Message, now time.Time) (bool, error) {
	if (m.Status != "pending_scan" && m.Status != "pending_review") || m.ReviewTaskID == nil || m.PeerEpoch == nil {
		return false, nil
	}
	s, e := LoadConversation(ctx, db, m.ConversationID)
	if errors.Is(e, command.ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if !s.CanRead(m.SenderID) {
		return false, nil
	}
	if e = Writable(ctx, db, s, now); e != nil {
		if errors.Is(e, command.ErrForbidden) || errors.Is(e, command.ErrBlockedInteraction) {
			return false, nil
		}
		return false, e
	}
	var t entity.ModerationTask
	if e = db.WithContext(ctx).Where("id=?", *m.ReviewTaskID).Take(&t).Error; e != nil {
		return false, e
	}
	sender, peer := s.Requester, s.Helper
	if sender.ID != m.SenderID {
		sender, peer = peer, sender
	}
	epoch, e := holdEpoch(ctx, db, s.Wish.ID)
	if e != nil {
		return false, e
	}
	if m.CancelEpoch != s.Conversation.WriteEpoch || t.SubjectEpoch != sender.AuthEpoch || *m.PeerEpoch != peer.AuthEpoch || t.RestrictionEpoch == nil || *t.RestrictionEpoch != s.Wish.RestrictionEpoch || t.HoldEpoch != epoch || (t.Status != "pending" && t.Status != "human_required") || t.ContentVersion != m.ContentVersion {
		return false, nil
	}
	var n int64
	e = db.WithContext(ctx).Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind='message' AND c.entity_id=? AND r.active", m.ID).Count(&n).Error
	return n == 0, e
}

// CancelMessage 只终结待审消息及审核绑定；调用方已持账号、会话、任务、消息锁。
// 本函数不重新获取工作锁，避免消费者持工作源时倒置锁序。
func CancelMessage(ctx context.Context, db *gorm.DB, m entity.Message) error {
	if m.Status != "pending_scan" && m.Status != "pending_review" {
		return nil
	}
	db = db.WithContext(ctx)
	if e := db.Model(&entity.Message{}).Where("id=? AND status IN ?", m.ID, []string{"pending_scan", "pending_review"}).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	if m.ReviewTaskID != nil {
		if e := db.Model(&entity.ModerationTask{}).Where("id=? AND status IN ?", *m.ReviewTaskID, []string{"pending", "human_required"}).Updates(map[string]any{"status": "obsolete", "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
	}
	return db.Model(&entity.ContentRegistry{}).Where("kind='message' AND entity_id=?", m.ID).Update("status", "cancelled").Error
}

// Deliver 在最终资格复核后分配事务计数器，并与正文可见性及站内通知一起提交。
// 计数器使用已锁会话行，事务回滚不消耗序号；重放不重复通知。
func Deliver(ctx context.Context, db *gorm.DB, m entity.Message, now time.Time) error {
	if m.Status == "delivered" {
		return nil
	}
	ok, e := EligibleMessage(ctx, db, m, now)
	if e != nil {
		return e
	}
	if !ok {
		return CancelMessage(ctx, db, m)
	}
	s, e := LoadConversation(ctx, db, m.ConversationID)
	if e != nil {
		return e
	}
	seq := s.Conversation.NextDeliverySeq
	if seq < 1 || seq == math.MaxInt64 {
		return command.ErrConflict
	}
	db = db.WithContext(ctx)
	r := db.Model(&entity.Conversation{}).Where("id=? AND next_delivery_seq=?", s.Conversation.ID, seq).Updates(map[string]any{"next_delivery_seq": seq + 1, "version": gorm.Expr("version+1")})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return command.ErrRetry
	}
	if e = db.Model(&entity.Message{}).Where("id=?", m.ID).Updates(map[string]any{"status": "delivered", "delivery_seq": seq, "delivered_at": now, "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	peer := s.Helper.ID
	if peer == m.SenderID {
		peer = s.Requester.ID
	}
	return db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: peer, Type: "message_delivered", TargetRef: s.Conversation.ID}).Error
}

// DecryptBody 只供已授权调用方读取私聊原文；不套用公开内容脱敏投影。
func DecryptBody(ctx context.Context, codec *securedata.Codec, m entity.Message) (string, error) {
	var envelope securedata.Envelope
	if json.Unmarshal(m.BodyCipher, &envelope) != nil {
		return "", command.ErrUnavailable
	}
	plain, e := codec.Decrypt(ctx, securedata.Binding{Purpose: "chat_message", ObjectID: m.ID, Version: uint64(m.ContentVersion)}, envelope)
	defer clear(plain)
	var body string
	if e != nil || json.Unmarshal(plain, &body) != nil {
		return "", command.ErrUnavailable
	}
	return body, nil
}
