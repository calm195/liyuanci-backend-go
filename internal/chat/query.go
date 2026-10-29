package chat

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Query 使用主体及用途绑定的游标；消息增量与列表游标不能同时提供。
type Query struct {
	Cursor   string
	AfterSeq *int64
	Limit    int
}

// Conversation 是当前原参与者可访问的最小元数据。
type Conversation struct {
	ID         string `json:"id"`
	Version    int64  `json:"version"`
	CanWrite   bool   `json:"can_write"`
	WriteEpoch int64  `json:"write_epoch"`
}

// ConversationsPage 不包含消息摘要或另一方身份快照。
type ConversationsPage struct {
	Items      []Conversation `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

// DeliveredMessage 仅在终审送达后包含原文。
type DeliveredMessage struct {
	MessageID   string    `json:"message_id"`
	DeliverySeq int64     `json:"delivery_seq"`
	SubmittedAt time.Time `json:"submitted_at"`
	DeliveredAt time.Time `json:"delivered_at"`
	Body        string    `json:"body"`
	// ViewerIsSender 仅相对当前会话标记本人消息，不泄露发送者账号标识。
	ViewerIsSender bool `json:"viewer_is_sender"`
}

// MessagesPage 的增量位置始终是实际返回的最后一条序号。
type MessagesPage struct {
	Items        []DeliveredMessage `json:"items"`
	NextAfterSeq int64              `json:"next_after_seq"`
}

// Status 是本人消息的当前结果，取消时不暴露另一方受罚详情。
type Status struct {
	MessageID    string             `json:"message_id"`
	Status       string             `json:"status"`
	DeliverySeq  *int64             `json:"delivery_seq"`
	ReviewResult *wish.ReviewResult `json:"review_result,omitempty"`
}

// Notification 是通用进展提示，没有私聊正文或作者身份。
type Notification struct {
	ID        string  `json:"id"`
	Version   int64   `json:"version"`
	Kind      string  `json:"kind"`
	Read      bool    `json:"read"`
	TargetRef *string `json:"target_ref,omitempty"`
	Notice    string  `json:"notice,omitempty"`
}

// NotificationsPage 使用本人独立游标，不共享会话列表位置。
type NotificationsPage struct {
	Items      []Notification `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}
type cursor struct {
	Scope string
	ID    string
	Seq   int64
}

func (s *Service) encode(c cursor) string {
	b, _ := json.Marshal(c)
	m := hmac.New(sha256.New, s.cfg.Key)
	m.Write([]byte("chat.cursor\x00"))
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (s *Service) decode(value, scope string) (out cursor, err error) {
	if value == "" {
		return out, nil
	}
	if len(value) > 2048 {
		return out, command.ErrInvalid
	}
	p := strings.Split(value, ".")
	if len(p) != 2 {
		return out, command.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil {
		return out, command.ErrInvalid
	}
	sig, e := base64.RawURLEncoding.DecodeString(p[1])
	m := hmac.New(sha256.New, s.cfg.Key)
	m.Write([]byte("chat.cursor\x00"))
	m.Write(b)
	if e != nil || !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(b, &out) != nil || out.Scope != scope || out.Seq < 0 || out.ID != "" && !validID(out.ID) {
		return cursor{}, command.ErrInvalid
	}
	return out, nil
}
func validQuery(q Query) bool {
	return q.Limit >= 1 && q.Limit <= 50 && (q.AfterSeq == nil || *q.AfterSeq >= 0) && !(q.Cursor != "" && q.AfterSeq != nil)
}
func (s *Service) read(ctx context.Context, token string, run func(*gorm.DB, string, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		if token == "" {
			return command.ErrUnauthenticated
		}
		h := sha256.Sum256([]byte(token))
		var ids []string
		if e := db.Table("public.sessions AS s").Select("a.id").Joins("JOIN public.accounts a ON a.id=s.account_id").Where("s.subject_kind='student' AND s.token_hash=? AND s.auth_epoch=a.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>? AND a.lifecycle='active'", h[:], now).Scan(&ids).Error; e != nil {
			return e
		}
		if len(ids) != 1 {
			return command.ErrUnauthenticated
		}
		return run(db, ids[0], now)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

// Conversations 列出原双方仍存续的会话，旧匹配只读，新的应愿人不能读取旧会话。
func (s *Service) Conversations(ctx context.Context, token, rid string, q Query) (out ConversationsPage, err error) {
	defer func() {
		if err != nil {
			out = ConversationsPage{}
		}
	}()
	defer s.done("conversations.list", rid, time.Now(), &err)
	if !validQuery(q) || q.AfterSeq != nil {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, func(db *gorm.DB, actor string, now time.Time) error {
		if e := readChatAllowed(db, actor, now); e != nil {
			return e
		}
		scope := "conversations/" + actor
		after, e := s.decode(q.Cursor, scope)
		if e != nil {
			return e
		}
		query := db.Table("public.conversations AS c").Select("c.*").Joins("JOIN public.matches m ON m.id=c.match_id").Joins("JOIN public.accounts a ON a.id=m.requester_id AND a.lifecycle='active'").Joins("JOIN public.accounts b ON b.id=m.helper_id AND b.lifecycle='active'").Joins("JOIN public.wishes w ON w.id=m.wish_id AND w.draft_deleted_at IS NULL").Where("(m.requester_id=? OR m.helper_id=?)", actor, actor)
		if after.ID != "" {
			query = query.Where("c.id<?", after.ID)
		}
		var rows []entity.Conversation
		if e = query.Order("c.id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Conversation{}
		if len(rows) > q.Limit {
			next := s.encode(cursor{Scope: scope, ID: rows[q.Limit-1].ID})
			out.NextCursor = &next
			rows = rows[:q.Limit]
		}
		for _, c := range rows {
			v, e := LoadConversation(ctx, db, c.ID)
			if e != nil {
				return e
			}
			e = Writable(ctx, db, v, now)
			if e != nil && !errors.Is(e, command.ErrForbidden) && !errors.Is(e, command.ErrBlockedInteraction) {
				return e
			}
			out.Items = append(out.Items, Conversation{c.ID, c.Version, e == nil, c.WriteEpoch})
		}
		return nil
	})
	return
}

// Messages 严格按最终送达号增量读取，慢审晚送达消息不会被提交时间游标漏掉。
func (s *Service) Messages(ctx context.Context, token, rid, id string, q Query) (out MessagesPage, err error) {
	defer func() {
		if err != nil {
			out = MessagesPage{}
		}
	}()
	defer s.done("messages.list", rid, time.Now(), &err)
	if !validID(id) || !validQuery(q) {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, func(db *gorm.DB, actor string, now time.Time) error {
		if e := readChatAllowed(db, actor, now); e != nil {
			return e
		}
		v, e := LoadConversation(ctx, db, id)
		if e != nil {
			return e
		}
		if !v.CanRead(actor) {
			return command.ErrNotFound
		}
		after, e := s.decode(q.Cursor, "messages/"+actor+"/"+id)
		if e != nil {
			return e
		}
		seq := after.Seq
		if q.AfterSeq != nil {
			seq = *q.AfterSeq
		}
		out.NextAfterSeq = seq
		out.Items = []DeliveredMessage{}
		var rows []entity.Message
		if e = db.Where("conversation_id=? AND status='delivered' AND delivery_seq>? AND EXISTS (SELECT 1 FROM public.content_registry approved WHERE approved.kind='message' AND approved.entity_id=messages.id AND approved.content_version=messages.content_version AND approved.status='approved') AND NOT EXISTS (SELECT 1 FROM public.content_registry cr JOIN public.content_restrictions r ON r.content_id=cr.id WHERE cr.kind='message' AND cr.entity_id=messages.id AND r.active)", id, seq).Order("delivery_seq").Limit(q.Limit).Find(&rows).Error; e != nil {
			return e
		}
		for _, m := range rows {
			if m.DeliverySeq == nil || m.DeliveredAt == nil {
				return command.ErrUnavailable
			}
			body, e := DecryptBody(ctx, s.codec, m)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, DeliveredMessage{m.ID, *m.DeliverySeq, m.CreatedAt, *m.DeliveredAt, body, m.SenderID == actor})
			out.NextAfterSeq = *m.DeliverySeq
		}
		return nil
	})
	return
}

// MessageStatus 只返回本人消息，不为收件人开放待审正文或状态枚举。
func (s *Service) MessageStatus(ctx context.Context, token, rid, id string) (out Status, err error) {
	defer func() {
		if err != nil {
			out = Status{}
		}
	}()
	defer s.done("message.status", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, func(db *gorm.DB, actor string, now time.Time) error {
		if e := readChatAllowed(db, actor, now); e != nil {
			return e
		}
		var m entity.Message
		if e := db.Where("id=? AND sender_id=?", id, actor).Take(&m).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrNotFound
			}
			return e
		}
		v, e := LoadConversation(ctx, db, m.ConversationID)
		if e != nil {
			return e
		}
		if !v.CanRead(actor) {
			return command.ErrNotFound
		}
		out = Status{MessageID: id, Status: m.Status}
		if m.Status == "pending_scan" || m.Status == "pending_review" {
			out.Status = "pending"
			ok, e := EligibleMessage(ctx, db, m, now)
			if e != nil {
				return e
			}
			if !ok {
				out.Status = "cancelled"
			}
		}
		if m.Status == "delivered" {
			out.DeliverySeq = m.DeliverySeq
		}
		if out.Status != "cancelled" && m.ReviewTaskID != nil {
			var t entity.ModerationTask
			if e = db.Where("id=?", *m.ReviewTaskID).Take(&t).Error; e != nil {
				return e
			}

			if t.CrisisRecheck && t.Status != "approved" {
				out.Status = "pending"
				if t.Status == "rejected" {
					out.Status = "rejected"
				}
				if t.Status == "cancelled" || t.Status == "obsolete" {
					out.Status = "cancelled"
				}
			}
			decision := "pending"
			if t.Status == "approved" || t.Status == "rejected" {
				decision = t.Status
			}
			out.ReviewResult = &wish.ReviewResult{Decision: decision, ContentVersion: m.ContentVersion}
			if decision == "rejected" {
				out.ReviewResult.Reason = t.Reason
				out.ReviewResult.RevisionSuggestion = t.RevisionSuggestion
			}
		}
		return nil
	})
	return
}
func notification(ctx context.Context, db *gorm.DB, row entity.Notification) (Notification, error) {
	out := Notification{ID: row.ID, Version: row.Version, Kind: row.Type, Read: row.ReadAt != nil, Notice: "有新进展，请打开应用查看"}
	if row.Type == "crisis_care" {
		var card entity.CareCard
		e := db.WithContext(ctx).Where("id=? AND recipient_id=?", row.TargetRef, row.RecipientID).Take(&card).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return out, e
		}
		if e == nil {
			ref := card.ID
			out.TargetRef = &ref
			out.Notice = "这里有可使用的援助资源，平台人工处理时段为北京时间8至22时"
		}
	}

	if row.Type == "penalty_notice" {
		var p entity.Penalty
		e := db.WithContext(ctx).Where("id=? AND account_id=?", row.TargetRef, row.RecipientID).Take(&p).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return out, e
		}
		if e == nil {
			ref := p.ID
			out.TargetRef = &ref
			out.Notice = "账号有新的处置，请查看警告或申诉入口"
		}
	}
	if row.Type == "appeal_result" {
		var a entity.Appeal
		e := db.WithContext(ctx).Where("id=? AND account_id=?", row.TargetRef, row.RecipientID).Take(&a).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return out, e
		}
		if e == nil {
			ref := a.ID
			out.TargetRef = &ref
		}
	}
	if row.Type == "message_delivered" {
		v, e := LoadConversation(ctx, db, row.TargetRef)
		if e != nil && !errors.Is(e, command.ErrNotFound) {
			return out, e
		}
		if e == nil && v.CanRead(row.RecipientID) {
			ref := row.TargetRef
			out.TargetRef = &ref
		}
	}
	return out, nil
}

// Notifications 仅返回本人通用提示，避免历史目标引用泄露已失效参与关系。
func (s *Service) Notifications(ctx context.Context, token, rid string, q Query) (out NotificationsPage, err error) {
	defer func() {
		if err != nil {
			out = NotificationsPage{}
		}
	}()
	defer s.done("notifications.list", rid, time.Now(), &err)
	if !validQuery(q) || q.AfterSeq != nil {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, func(db *gorm.DB, actor string, now time.Time) error {
		scope := "notifications/" + actor
		after, e := s.decode(q.Cursor, scope)
		if e != nil {
			return e
		}
		query := db.Where("recipient_id=? AND type NOT IN ('whisper_comment','whisper_capsule_unlocked')", actor)
		if e := readChatAllowed(db, actor, now); errors.Is(e, command.ErrForbidden) {
			query = query.Where("type IN ('penalty_notice','appeal_result','crisis_care')")
		} else if e != nil {
			return e
		}
		if after.ID != "" {
			query = query.Where("id<?", after.ID)
		}
		var rows []entity.Notification
		if e = query.Order("id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Notification{}
		if len(rows) > q.Limit {
			next := s.encode(cursor{Scope: scope, ID: rows[q.Limit-1].ID})
			out.NextCursor = &next
			rows = rows[:q.Limit]
		}
		for _, r := range rows {
			item, e := notification(ctx, db, r)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	return
}

// ReadNotification 以通知自身版本幂等标记已读，不更新其他人的通知。
func (s *Service) ReadNotification(ctx context.Context, token, key, rid, id string, version int64) (out Notification, err error) {
	defer func() {
		if err != nil {
			out = Notification{}
		}
	}()
	defer s.done("notification.read", rid, time.Now(), &err)
	if !validID(id) || version < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "notification.read", Method: "POST", ResourcePath: "/api/v1/me/notifications/" + id + "/read", ActorKind: "student", Access: command.Restricted, RequiredExpectations: []string{"expected_version"}, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		var n int64
		if e := db.WithContext(ctx).Model(&entity.Notification{}).Where("id=?", id).Count(&n).Error; e != nil {
			return command.Discovery{}, e
		}
		p := command.Discovery{}
		if n > 0 {
			p.Resources = []command.Resource{{Kind: command.Notification, ID: id}}
		}
		return p, nil
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		var n int64
		if e := tx.DB().WithContext(ctx).Model(&entity.Notification{}).Where("id=? AND recipient_id=? AND type NOT IN ('whisper_comment','whisper_capsule_unlocked')", id, tx.Actor().ID).Count(&n).Error; e != nil {
			return e
		}
		if n != 1 {
			return command.ErrNotFound
		}
		if e := readChatAllowed(tx.DB().WithContext(ctx), tx.Actor().ID, tx.Now()); e != nil {
			if !errors.Is(e, command.ErrForbidden) {
				return e
			}
			var row entity.Notification
			if e := tx.DB().WithContext(ctx).Where("id=?", id).Take(&row).Error; e != nil {
				return e
			}
			if row.Type != "penalty_notice" && row.Type != "appeal_result" && row.Type != "crisis_care" {
				return command.ErrForbidden
			}
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		e := tx.DB().WithContext(ctx).Model(&entity.Notification{}).Where("id=? AND read_at IS NULL", id).Updates(map[string]any{"read_at": tx.Now(), "version": gorm.Expr("version+1")}).Error
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var row entity.Notification
		if e := tx.DB().WithContext(ctx).Where("id=? AND recipient_id=? AND type NOT IN ('whisper_comment','whisper_capsule_unlocked')", ref, tx.Actor().ID).Take(&row).Error; e != nil {
			return e
		}
		var e error
		out, e = notification(ctx, tx.DB(), row)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: version, Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.Notification, ID: id}, Value: version}}})
	return
}

// readChatAllowed 允许禁言账号读取历史，永久封禁只保留申诉及处置通知视图。
func readChatAllowed(db *gorm.DB, actor string, now time.Time) error {
	var n int64
	if e := db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind='ban' AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", actor, now, now).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrForbidden
	}
	return nil
}
