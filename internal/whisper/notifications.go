package whisper

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// AppendCommentNotice 在最终首次公开事务内插入一次通知；后续重审和自评论均不重复。
// 调用者须持评论作者、帖主账号 guard 及父帖／评论锁；数据库唯一键保护事件重放。
func AppendCommentNotice(ctx context.Context, db *gorm.DB, id string, now time.Time) error {
	db = db.WithContext(ctx)
	var c entity.WhisperComment
	if e := db.Select("id,post_id,author_id").Where("id=?", id).Take(&c).Error; e != nil {
		return e
	}
	if c.PostID == nil {
		return command.ErrNotFound
	}
	var p entity.WhisperPost
	if e := db.Select("owner_id").Where("id=?", *c.PostID).Take(&p).Error; e != nil {
		return e
	}
	if p.OwnerID == c.AuthorID {
		return nil
	}
	var n int64
	if e := db.Model(&entity.WhisperNotificationRef{}).Where("kind='whisper_comment' AND source_id=?", id).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	notificationID := uuid.NewString()
	if e := db.Create(&entity.Notification{ID: notificationID, RecipientID: p.OwnerID, Type: "whisper_comment", TargetRef: *c.PostID, CreatedAt: now}).Error; e != nil {
		return e
	}
	return db.Create(&entity.WhisperNotificationRef{NotificationID: notificationID, Kind: "whisper_comment", SourceID: id, TargetKind: "post", TargetID: *c.PostID, CommentID: &id}).Error
}

func notificationProjection(n entity.Notification, r entity.WhisperNotificationRef) Notification {
	out := Notification{ID: n.ID, Version: n.Version, Kind: r.Kind, TargetKind: r.TargetKind, TargetID: r.TargetID, CommentID: r.CommentID, Read: n.ReadAt != nil, CreatedAt: timestamp(n.CreatedAt), Notice: "有新的暖评，请打开帖子查看", AllowedActions: []string{}}
	if r.Kind == "whisper_capsule_unlocked" {
		out.Notice = "时空胶囊已到约定时间，请打开查看"
	}
	if !out.Read {
		out.AllowedActions = []string{"whisperNotificationRead"}
	}
	return out
}

// Notifications 仅提供本模块本人通知，无旧正文或作者映射；目标访问由详情入口重验。
func (s *Service) Notifications(ctx context.Context, token, rid string, q Query) (out Page[Notification], err error) {
	defer s.completed("notifications_list", rid, time.Now(), &err)
	if !s.config.Enabled {
		return out, ErrModuleUnavailable
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor, e := reader(ctx, db, token, now, false)
		if e != nil {
			return e
		}
		scope := s.scope("notifications", actor.ID)
		after, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		query := db.WithContext(ctx).Model(&entity.Notification{}).Where("recipient_id=? AND id IN (SELECT notification_id FROM public.whisper_notification_refs WHERE NOT suppressed)", actor.ID)
		if after.ID != "" {
			query = query.Where("(created_at,id)<(?,?)", after.At, after.ID)
		}
		var rows []entity.Notification
		if e = query.Order("created_at DESC,id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Notification{}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			last := rows[len(rows)-1]
			v := s.encodeCursor(cursor{scope, last.ID, last.CreatedAt})
			out.NextCursor = &v
		}
		for _, n := range rows {
			var r entity.WhisperNotificationRef
			if e = db.WithContext(ctx).Where("notification_id=?", n.ID).Take(&r).Error; e != nil {
				return e
			}
			out.Items = append(out.Items, notificationProjection(n, r))
		}
		return nil
	})
	if err != nil {
		out = Page[Notification]{}
	}
	return
}

// ReadNotification 只修改已读状态，不因目标不可访问而恢复旧内容。
func (s *Service) ReadNotification(ctx context.Context, token, key, rid, id string, version int64) (out Notification, err error) {
	defer s.completed("notification_read", rid, time.Now(), &err)
	if !ValidID(id) || version < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "whisper.notification.read", Method: "POST", ResourcePath: "/api/v1/me/whispers/notifications/" + id + "/read", ActorKind: "student", Access: command.Restricted, RetainReceipt: true, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
		return command.Discovery{Resources: []command.Resource{{Kind: command.Notification, ID: id}}}, nil
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		var n int64
		e := tx.DB().WithContext(ctx).Model(&entity.Notification{}).Where("id=? AND recipient_id=? AND id IN (SELECT notification_id FROM public.whisper_notification_refs WHERE NOT suppressed)", id, tx.Actor().ID).Count(&n).Error
		if e != nil {
			return e
		}
		if n != 1 {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		e := tx.DB().WithContext(ctx).Model(&entity.Notification{}).Where("id=? AND read_at IS NULL", id).Updates(map[string]any{"read_at": tx.Now(), "version": gorm.Expr("version+1")}).Error
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var n entity.Notification
		var r entity.WhisperNotificationRef
		if e := tx.DB().WithContext(ctx).Where("id=? AND recipient_id=?", ref, tx.Actor().ID).Take(&n).Error; e != nil {
			return missingRow(e)
		}
		if e := tx.DB().WithContext(ctx).Where("notification_id=? AND NOT suppressed", ref).Take(&r).Error; e != nil {
			return missingRow(e)
		}
		out = notificationProjection(n, r)
		return nil
	}
	_, err = s.execute(ctx, d, token, key, rid, struct{ Version int64 }{version}, 0, "", "maintenance", command.Expectation{Name: "expected_version", Target: command.Resource{Kind: command.Notification, ID: id}, Value: version})
	return
}
