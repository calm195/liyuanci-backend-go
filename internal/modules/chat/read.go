package chat

import (
	"context"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"gorm.io/gorm"
)

// 可见消息的摘要、未读判断与详情共用终审和限制过滤，待审或已屏蔽正文不可进入列表。
func visibleMessages(db *gorm.DB, id string) *gorm.DB {
	return db.Model(&entity.Message{}).Where("conversation_id=? AND status='delivered' AND delivery_seq IS NOT NULL AND delivered_at IS NOT NULL AND EXISTS (SELECT 1 FROM public.content_registry approved WHERE approved.kind='message' AND approved.entity_id=messages.id AND approved.content_version=messages.content_version AND approved.status='approved') AND NOT EXISTS (SELECT 1 FROM public.content_registry cr JOIN public.content_restrictions r ON r.content_id=cr.id WHERE cr.kind='message' AND cr.entity_id=messages.id AND r.active)", id)
}

// MessagePreview 只包含当前参与者可读的最新正文摘要及本人方向。
type MessagePreview struct {
	Body           string `json:"body"`
	DeliverySeq    int64  `json:"delivery_seq"`
	ViewerIsSender bool   `json:"viewer_is_sender"`
}

func (s *Service) conversationPreview(ctx context.Context, db *gorm.DB, actor string, out *Conversation) error {
	var read entity.ConversationRead
	if e := db.Where("conversation_id=? AND account_id=?", out.ID, actor).Limit(1).Find(&read).Error; e != nil {
		return e
	}
	out.LastReadSeq = read.LastReadSeq
	var unread int64
	if e := visibleMessages(db, out.ID).Where("sender_id<>? AND delivery_seq>?", actor, read.LastReadSeq).Limit(1).Select("1").Scan(&unread).Error; e != nil {
		return e
	}
	out.HasUnread = unread != 0
	var rows []entity.Message
	if e := visibleMessages(db, out.ID).Order("delivery_seq DESC").Limit(1).Find(&rows).Error; e != nil {
		return e
	}
	if len(rows) == 0 {
		return nil
	}
	m := rows[0]
	body, e := DecryptBody(ctx, s.codec, m)
	if e != nil {
		return e
	}
	runes := []rune(body)
	if len(runes) > 120 {
		body = string(runes[:120]) + "…"
	}
	out.LatestMessage = &MessagePreview{Body: body, DeliverySeq: *m.DeliverySeq, ViewerIsSender: m.SenderID == actor}
	return nil
}

// ConversationReadReceipt 仅向当前鉴权阅读者返回单调阅读位置。
type ConversationReadReceipt struct {
	ID          string `json:"id"`
	LastReadSeq int64  `json:"last_read_seq"`
}

// ReadConversation 显式确认页面已展示的位置；读取列表和消息 GET 均不产生已读副作用。
func (s *Service) ReadConversation(ctx context.Context, token, key, rid, id string, seq int64) (out ConversationReadReceipt, err error) {
	defer func() {
		if err != nil {
			out = ConversationReadReceipt{}
		}
	}()
	defer s.done("conversation.read", rid, time.Now(), &err)
	if !validID(id) || seq < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "conversation.read", Method: "POST", ResourcePath: "/api/v1/conversations/" + id + "/read", ActorKind: "student", Access: command.Restricted}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverConversation(ctx, db, id)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		if e := readChatAllowed(tx.DB().WithContext(ctx), tx.Actor().ID, tx.Now()); e != nil {
			return e
		}
		v, e := LoadConversation(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		return v.AuthorizeRead(ctx, tx.DB(), tx.Actor().ID, tx.Now())
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		var n int64
		if e := visibleMessages(db, id).Where("delivery_seq=?", seq).Count(&n).Error; e != nil {
			return "", e
		}
		if n != 1 {
			return "", command.ErrInvalid
		}
		// 双方账户和会话沿用统一锁序；旧页面/重复请求只能推进自己的位置，不能覆盖较新的已读。
		e := db.Exec(`INSERT INTO public.conversation_reads(conversation_id,account_id,last_read_seq,updated_at) VALUES(?,?,?,?) ON CONFLICT(conversation_id,account_id) DO UPDATE SET last_read_seq=EXCLUDED.last_read_seq,updated_at=EXCLUDED.updated_at WHERE conversation_reads.last_read_seq<EXCLUDED.last_read_seq`, id, tx.Actor().ID, seq, tx.Now()).Error
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var row entity.ConversationRead
		if e := tx.DB().WithContext(ctx).Where("conversation_id=? AND account_id=?", ref, tx.Actor().ID).Take(&row).Error; e != nil {
			return e
		}
		out = ConversationReadReceipt{ID: ref, LastReadSeq: row.LastReadSeq}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: seq})
	return
}
