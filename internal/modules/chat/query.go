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

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/contentreview"
)

// Query 使用主体及用途绑定的游标；消息增量与列表游标不能同时提供。
type Query struct {
	Cursor   string
	AfterSeq *int64
	Limit    int
}

// Conversation 是当前原参与者可访问的最小元数据。
type Conversation struct {
	WishTitle  string `json:"wish_title"`
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
	MessageID    string                `json:"message_id"`
	Status       string                `json:"status"`
	DeliverySeq  *int64                `json:"delivery_seq"`
	ReviewResult *contentreview.Result `json:"review_result,omitempty"`
}

// Notification 是通用进展提示，没有私聊正文或作者身份。
type Notification struct {
	CreatedAt  string  `json:"created_at"`
	TargetKind *string `json:"target_kind,omitempty"`
	CommentID  *string `json:"comment_id,omitempty"`
	ID         string  `json:"id"`
	Version    int64   `json:"version"`
	Kind       string  `json:"kind"`
	Read       bool    `json:"read"`
	TargetRef  *string `json:"target_ref,omitempty"`
	Notice     string  `json:"notice,omitempty"`
}

// NotificationsPage 使用本人独立游标，不共享会话列表位置。
type NotificationsPage struct {
	Items      []Notification `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

type cursor struct {
	At    time.Time `json:"At,omitempty"`
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
	if e != nil || !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(b, &out) != nil || out.Scope != scope || out.Seq < 0 ||
		out.ID != "" && !validID(out.ID) {
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
		if e := db.Table("public.sessions AS s").Select("a.id").Joins("JOIN public.accounts a ON a.id=s.account_id").Where(
			"s.subject_kind='student' AND s.token_hash=? AND s.auth_epoch=a.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>? AND a.lifecycle='active'",
			h[:],
			now,
		).Scan(&ids).Error; e != nil {
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
		// 标题与会话列表一次读取，仅读取当前已审且未被隐私屏障撤销的投影。
		titleProjection := `c.*,
 CASE WHEN c.matchmaking_pair_id IS NOT NULL THEN ma.title
 WHEN v.moderation_status='approved' AND v.public_body IS NOT NULL
 AND NOT EXISTS (
  SELECT 1 FROM public.privacy_holds h
  WHERE h.target_kind='wish' AND h.target_id=w.id AND h.status='active'
 ) AND NOT EXISTS (
  SELECT 1 FROM public.content_restrictions x
  JOIN public.content_registry cr ON cr.id=x.content_id
  WHERE cr.kind='wish' AND cr.entity_id=w.id AND x.active
 ) THEN COALESCE(NULLIF(v.public_title,''),'未命名心愿')
 ELSE '未命名心愿' END AS wish_title`
		query := db.Table("public.conversations AS c").Select(titleProjection).
			Joins("LEFT JOIN public.matches m ON m.id=c.match_id").
			Joins("LEFT JOIN public.accounts a ON a.id=m.requester_id AND a.lifecycle='active'").
			Joins("LEFT JOIN public.accounts b ON b.id=m.helper_id AND b.lifecycle='active'").
			Joins("LEFT JOIN public.wishes w ON w.id=m.wish_id AND w.draft_deleted_at IS NULL").
			Joins("LEFT JOIN public.wish_versions v ON v.wish_id=w.id AND v.content_version=w.current_content_version").
			Joins("LEFT JOIN public.matchmaking_pairs mp ON mp.id=c.matchmaking_pair_id").
			Joins("LEFT JOIN public.matchmaking_activities ma ON ma.id=mp.activity_id").
			Where("(a.id IS NOT NULL AND b.id IS NOT NULL AND w.id IS NOT NULL AND (m.requester_id=? OR m.helper_id=?)) OR ("+matchmakingConversationListFilter+")", actor, actor, actor, now, now)
		if after.ID != "" {
			query = query.Where("c.id<?", after.ID)
		}
		var rows []struct {
			entity.Conversation
			WishTitle string
		}
		if e = query.Order("c.id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Conversation{}
		if len(rows) > q.Limit {
			next := s.encode(cursor{Scope: scope, ID: rows[q.Limit-1].ID})
			out.NextCursor = &next
			rows = rows[:q.Limit]
		}
		for _, row := range rows {
			c := row.Conversation
			v, e := LoadConversation(ctx, db, c.ID)
			if e != nil {
				return e
			}
			e = Writable(ctx, db, v, now)
			if e != nil && !errors.Is(e, command.ErrForbidden) && !errors.Is(e, command.ErrBlockedInteraction) {
				return e
			}
			out.Items = append(out.Items, Conversation{
				WishTitle:  row.WishTitle,
				ID:         c.ID,
				Version:    c.Version,
				CanWrite:   e == nil,
				WriteEpoch: c.WriteEpoch,
			})
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
		if e = v.AuthorizeRead(ctx, db, actor, now); e != nil {
			return e
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
		if e = db.Where(
			"conversation_id=? AND status='delivered' AND delivery_seq>? AND EXISTS (SELECT 1 FROM public.content_registry approved WHERE approved.kind='message' AND approved.entity_id=messages.id AND approved.content_version=messages.content_version AND approved.status='approved') AND NOT EXISTS (SELECT 1 FROM public.content_registry cr JOIN public.content_restrictions r ON r.content_id=cr.id WHERE cr.kind='message' AND cr.entity_id=messages.id AND r.active)",
			id,
			seq,
		).Order("delivery_seq").Limit(q.Limit).Find(&rows).Error; e != nil {
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
		if e = v.AuthorizeRead(ctx, db, actor, now); e != nil {
			return e
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
			out.ReviewResult = &contentreview.Result{Decision: decision, ContentVersion: m.ContentVersion}
			if decision == "rejected" {
				out.ReviewResult.Reason = t.Reason
				out.ReviewResult.RevisionSuggestion = t.RevisionSuggestion
			}
		}
		return nil
	})
	return
}

func (s *Service) notification(ctx context.Context, db *gorm.DB, row entity.Notification, now time.Time) (Notification, error) {
	out := Notification{
		ID:        row.ID,
		Version:   row.Version,
		Kind:      row.Type,
		Read:      row.ReadAt != nil,
		Notice:    "这条通知的详情暂不可查看。",
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
	}
	switch row.Type {
	case "matchmaking_review", "matchmaking_published", "matchmaking_ended":
		if s.matchmakingNotificationTarget != nil {
			kind, ref, err := s.matchmakingNotificationTarget(ctx, db, row, now)
			if err != nil {
				return out, err
			}
			if kind != "" && ref != "" {
				out.TargetKind, out.TargetRef = &kind, &ref
			}
		}
		out.Notice = "包分配活动状态已更新，请查看活动。"
	case "application_active", "application_accepted", "application_rejected", "application_invalidated":
		label := "心愿"
		if s.applicationNotificationTarget != nil {
			kind, ref, title, err := s.applicationNotificationTarget(ctx, db, row, now)
			if err != nil {
				return out, err
			}
			if kind != "" && ref != "" {
				out.TargetKind, out.TargetRef = &kind, &ref
			}
			if title != "" {
				label += "《" + title + "》"
			}
		}
		switch row.Type {
		case "application_active":
			out.Notice = "你的" + label + "收到新的应愿申请。"
		case "application_accepted":
			out.Notice = "你对" + label + "的应愿申请已被许愿人接受。"
		case "application_invalidated":
			out.Notice = "应愿申请已失效，请查看申请状态。"
		case "application_rejected":
			out.Notice = "你对" + label + "的应愿留言审核未通过，可查看申请结果。"
		}
	case "wish_comment":
		if s.commentNotificationTarget != nil {
			ref, comment, e := s.commentNotificationTarget(ctx, db, row, now)
			if e != nil {
				return out, e
			}
			if ref != "" && comment != "" {
				kind := "wish"
				out.TargetKind, out.TargetRef, out.CommentID = &kind, &ref, &comment
				out.Notice = "心愿收到了新的评论或回复。"
			}
		}
	case "wish_published", "wish_rejected":
		var n int64
		if e := db.Model(&entity.Wish{}).Where("id=? AND owner_id=? AND draft_deleted_at IS NULL", row.TargetRef, row.RecipientID).Count(&n).Error; e != nil {
			return out, e
		}
		if n == 1 {
			kind, ref := "wish", row.TargetRef
			out.TargetKind, out.TargetRef = &kind, &ref
		}
		out.Notice = "心愿已发布。"
		if row.Type == "wish_rejected" {
			out.Notice = "心愿审核未通过，请查看修改建议。"
		}
	case "message_reviewed":
		out.Notice = "你发送的聊天消息未通过审核，请查看聊天。"
	case "message_delivered":
		out.Notice = "你收到了一条新聊天消息。"
	}

	if row.Type == "whisper_comment" || row.Type == "whisper_capsule_unlocked" {
		var ref entity.WhisperNotificationRef
		e := db.WithContext(ctx).Where("notification_id=? AND NOT suppressed", row.ID).Take(&ref).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return out, e
		}
		if e == nil {
			out.TargetKind = &ref.TargetKind
			out.TargetRef = &ref.TargetID
			out.CommentID = ref.CommentID
			out.Notice = "有新的暖评，请打开帖子查看"
			if row.Type == "whisper_capsule_unlocked" {
				out.Notice = "时光胶囊已到约定时间，请打开查看"
			}
		}
	}
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
			out.Notice = "你的申诉已处理，请查看申诉结果。"
			if a.Status == "accepted" {
				out.Notice = "你的申诉已通过，相关处置已撤销。"
			} else if a.Status == "rejected" {
				out.Notice = "你的申诉未通过，请查看申诉结果。"
			}
		}
	}
	if row.Type == "message_delivered" || row.Type == "message_reviewed" {
		conversationID := row.TargetRef
		if row.Type == "message_reviewed" {
			var message entity.Message
			e := db.WithContext(ctx).Where("id=? AND sender_id=?", row.TargetRef, row.RecipientID).Take(&message).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return out, nil
			}
			if e != nil {
				return out, e
			}
			conversationID = message.ConversationID
		}
		v, e := LoadConversation(ctx, db, conversationID)
		if e != nil && !errors.Is(e, command.ErrNotFound) {
			return out, e
		}
		if e != nil {
			return out, nil
		}
		e = v.AuthorizeRead(ctx, db, row.RecipientID, now)
		if e != nil && !errors.Is(e, command.ErrNotFound) {
			return out, e
		}
		if e == nil {
			kind := "conversation"
			out.TargetKind = &kind
			out.TargetRef = &conversationID
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
		scope := "notifications.timeline/" + actor
		after, e := s.decode(q.Cursor, scope)
		if e != nil {
			return e
		}
		query, e := notificationScope(db, actor, now)
		if e != nil {
			return e
		}
		if after.ID != "" {
			query = query.Where("(created_at,id)<(?,?)", after.At, after.ID)
		}
		var rows []entity.Notification
		if e = query.Order("created_at DESC,id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Notification{}
		if len(rows) > q.Limit {
			next := s.encode(cursor{Scope: scope, ID: rows[q.Limit-1].ID, At: rows[q.Limit-1].CreatedAt})
			out.NextCursor = &next
			rows = rows[:q.Limit]
		}
		for _, r := range rows {
			item, e := s.notification(ctx, db, r, now)
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
	d := command.Definition{
		Kind:                 "notification.read",
		Method:               "POST",
		ResourcePath:         "/api/v1/me/notifications/" + id + "/read",
		ActorKind:            "student",
		Access:               command.Restricted,
		RequiredExpectations: []string{"expected_version"},
		Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
			var n int64
			if e := db.WithContext(ctx).Model(&entity.Notification{}).Where("id=?", id).Count(&n).Error; e != nil {
				return command.Discovery{}, e
			}
			p := command.Discovery{}
			if n > 0 {
				p.Resources = []command.Resource{{Kind: command.Notification, ID: id}}
			}
			return p, nil
		},
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		var n int64
		if e := tx.DB().WithContext(ctx).Model(&entity.Notification{}).Where(
			"id=? AND recipient_id=? AND NOT EXISTS(SELECT 1 FROM public.whisper_notification_refs r WHERE r.notification_id=notifications.id AND r.suppressed)",
			id,
			tx.Actor().ID,
		).Count(&n).Error; e != nil {
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
		e := tx.DB().WithContext(ctx).Model(&entity.Notification{}).
			Where("id=? AND read_at IS NULL", id).
			Updates(map[string]any{"read_at": tx.Now(), "version": gorm.Expr("version+1")}).
			Error
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var row entity.Notification
		if e := tx.DB().WithContext(ctx).Where(
			"id=? AND recipient_id=? AND NOT EXISTS(SELECT 1 FROM public.whisper_notification_refs r WHERE r.notification_id=notifications.id AND r.suppressed)",
			ref,
			tx.Actor().ID,
		).Take(&row).Error; e != nil {
			return e
		}
		var e error
		out, e = s.notification(ctx, tx.DB(), row, tx.Now())
		return e
	}
	_, err = s.engine.Execute(
		ctx,
		d,
		command.Input{
			Token:          token,
			IdempotencyKey: key,
			RequestID:      rid,
			Request:        version,
			Expectations: []command.Expectation{{
				Name:   "expected_version",
				Target: command.Resource{Kind: command.Notification, ID: id},
				Value:  version,
			}},
		},
	)
	return
}

// readChatAllowed 允许禁言账号读取历史，永久封禁只保留申诉及处置通知视图。
func readChatAllowed(db *gorm.DB, actor string, now time.Time) error {
	var n int64
	if e := db.Model(&entity.Penalty{}).Where(
		"account_id=? AND action_kind='ban' AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)",
		actor,
		now,
		now,
	).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrForbidden
	}
	return nil
}
