package whisper

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// CreateCapsule 在账号guard下检查活动额度、加密封存并同事务安排安全审核与预约工作。
func (s *Service) CreateCapsule(ctx context.Context, token, key, rid string, in CapsuleInput) (out Capsule, err error) {
	defer s.completed("capsule_create", rid, time.Now(), &err)
	if !validText(in.Body, 1, 500) || !in.SafetyNoticeAccepted || in.UnlockAt.IsZero() || in.UnlockAt.Nanosecond() != 0 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "whisper.capsule.create", Method: "POST", ResourcePath: "/api/v1/me/whispers/capsules", ActorKind: "student", Access: command.Ordinary, RetainReceipt: true}
	d.Discover = func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
		return command.Discovery{}, nil
	}
	// 账号存续及普通写权限由引擎复核；创建不接受任何外部主体引用。
	d.Authorize = func(context.Context, *command.Tx) error { return nil }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		if !validCapsuleTime(tx.Now(), in.UnlockAt) {
			return "", command.ErrInvalid
		}
		var count int64
		if e := tx.DB().WithContext(ctx).Model(&entity.WhisperCapsule{}).Where("owner_id=? AND lifecycle='active'", tx.Actor().ID).Count(&count).Error; e != nil {
			return "", e
		}
		if count >= 20 {
			return "", ErrCapsuleLimit
		}
		id, reg := uuid.NewString(), uuid.NewString()
		body, e := s.encrypt(ctx, "whisper_capsule_body", id, 1, in.Body)
		if e != nil {
			return "", e
		}
		if e = registry(ctx, tx.DB(), reg, "whisper_capsule", id, tx.Actor().ID, 1); e != nil {
			return "", e
		}
		task, e := s.submit(ctx, tx, reg, "whisper_capsule", 1, 1, 0, 0)
		if e != nil {
			return "", e
		}
		c := entity.WhisperCapsule{ID: id, OwnerID: tx.Actor().ID, ContentRegistryID: reg, BodyCipher: body, UnlockAt: in.UnlockAt, ReviewRound: 1, ReviewTaskID: &task, CreatedAt: tx.Now().UTC().Truncate(time.Second)}
		if e = insertRow(ctx, tx.DB(), &c); e != nil {
			return "", e
		}
		if e = s.scheduleCapsule(ctx, tx.DB(), id, in.UnlockAt); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.capsule(ctx, tx.DB(), tx.Now(), ref, tx.Actor().ID, false)
		return e
	}
	_, err = s.execute(ctx, d, token, key, rid, in, 0, "", "capsule")
	return
}

func capsuleRow(ctx context.Context, db *gorm.DB, id string) (c entity.WhisperCapsule, err error) {
	err = missingRow(db.WithContext(ctx).Where("id=?", id).Take(&c).Error)
	return
}

type capsuleFlags struct{ Privacy, Restricted bool }

func capsuleBarriers(ctx context.Context, db *gorm.DB, id, registryID string) (f capsuleFlags, err error) {
	err = db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM public.privacy_holds WHERE target_kind='whisper_capsule' AND target_id=? AND status='active') AS privacy,EXISTS(SELECT 1 FROM public.content_restrictions WHERE content_id=? AND active) AS restricted`, id, registryID).Scan(&f).Error
	return
}

func capsuleStatus(c entity.WhisperCapsule, flags capsuleFlags, now time.Time) string {
	if c.Lifecycle != "active" {
		return c.Lifecycle
	}
	// Wire无独立隐私状态；所有保护屏障统一投影isolated，不透露具体保护原因。
	if flags.Privacy || flags.Restricted {
		return "isolated"
	}
	switch c.ReviewState {
	case "approved":
		if now.Before(c.UnlockAt) {
			return "locked"
		}
		return "unlocked"
	case "pending":
		return "review_pending"
	case "rejected":
		return "rejected"
	case "cancelled":
		return "scanning"
	}
	return "scanning"
}

func (s *Service) capsule(ctx context.Context, db *gorm.DB, now time.Time, id, owner string, detail bool) (out Capsule, err error) {
	c, e := capsuleRow(ctx, db, id)
	if e != nil {
		return out, e
	}
	if c.OwnerID != owner {
		return out, command.ErrNotFound
	}
	flags, e := capsuleBarriers(ctx, db, id, c.ContentRegistryID)
	if e != nil {
		return out, e
	}
	state := capsuleStatus(c, flags, now)
	out = Capsule{ID: id, Version: c.Version, Status: state, UnlockAt: timestamp(c.UnlockAt), ServerNow: timestamp(now), CreatedAt: timestamp(c.CreatedAt), AllowedActions: []string{}}
	if c.Lifecycle == "active" {
		if now.Before(c.UnlockAt) {
			out.AllowedActions = append(out.AllowedActions, "whisperCapsuleCancel")
		}
		if !now.Before(c.UnlockAt) || state == "rejected" || state == "isolated" {
			out.AllowedActions = append(out.AllowedActions, "whisperCapsuleDelete")
		}
		if c.ReviewTaskID != nil && !flags.Privacy {
			var t entity.ModerationTask
			if e = reviewTask(ctx, db, *c.ReviewTaskID, c.ContentRegistryID, 1, &t); e != nil {
				return Capsule{}, e
			}
			decision := "pending"
			if t.Status == "approved" || t.Status == "rejected" {
				decision = t.Status
			}
			out.ReviewResult = &ReviewResult{Decision: decision, ContentVersion: 1}
			if decision == "rejected" {
				out.ReviewResult.Reason = t.Reason
				out.ReviewResult.RevisionSuggestion = t.RevisionSuggestion
			}
		}
	}
	if detail && state == "unlocked" {
		var body string
		if e = s.decrypt(ctx, "whisper_capsule_body", id, 1, c.BodyCipher, &body); e != nil {
			return Capsule{}, e
		}
		out.Body = &body
	}
	return out, nil
}

// GetCapsule 每次按当前数据库时间和保护屏障计算读取资格；普通处罚不禁止本人读取。
func (s *Service) GetCapsule(ctx context.Context, token, rid, id string) (out Capsule, err error) {
	defer s.completed("capsule_get", rid, time.Now(), &err)
	if !s.config.Enabled {
		return out, ErrModuleUnavailable
	}
	if !ValidID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		a, e := reader(ctx, db, token, now, false)
		if e != nil {
			return e
		}
		out, e = s.capsule(ctx, db, now, id, a.ID, true)
		return e
	})
	if err != nil {
		out = Capsule{}
	}
	return
}

// ListCapsules 即使到期也不解密正文，使用本人独立的创建时间倒序游标。
func (s *Service) ListCapsules(ctx context.Context, token, rid string, q Query) (out Page[Capsule], err error) {
	defer s.completed("capsules_list", rid, time.Now(), &err)
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
		a, e := reader(ctx, db, token, now, false)
		if e != nil {
			return e
		}
		scope := s.scope("capsules", a.ID)
		after, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		query := db.WithContext(ctx).Model(&entity.WhisperCapsule{}).Select("id,created_at").Where("owner_id=?", a.ID)
		if after.ID != "" {
			query = query.Where("(created_at,id)<(?,?)", after.At, after.ID)
		}
		var rows []entity.WhisperCapsule
		if e = query.Order("created_at DESC,id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Capsule{}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			last := rows[len(rows)-1]
			v := s.encodeCursor(cursor{scope, last.ID, last.CreatedAt})
			out.NextCursor = &v
		}
		for _, c := range rows {
			v, e := s.capsule(ctx, db, now, c.ID, a.ID, false)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	if err != nil {
		out = Page[Capsule]{}
	}
	return
}

// CloseCapsule 按服务器时间取消或删除普通副本；终态回执不保存正文，独立证据不受影响。
func (s *Service) CloseCapsule(ctx context.Context, token, key, rid, id, action string, version int64) (out Capsule, err error) {
	defer s.completed("capsule_"+action, rid, time.Now(), &err)
	if !ValidID(id) || version < 1 || (action != "cancel" && action != "delete") {
		return out, command.ErrInvalid
	}
	target := command.Resource{Kind: command.WhisperCapsule, ID: id}
	d := command.Definition{Kind: "whisper.capsule." + action, Method: "POST", ResourcePath: "/api/v1/me/whispers/capsules/" + id + "/" + action, ActorKind: "student", Access: command.Restricted, RetainReceipt: true, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
		return command.Discovery{Resources: []command.Resource{target}}, nil
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		c, e := capsuleRow(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		if c.OwnerID != tx.Actor().ID {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		c, e := capsuleRow(ctx, db, id)
		if e != nil {
			return "", e
		}
		flags, e := capsuleBarriers(ctx, db, id, c.ContentRegistryID)
		if e != nil {
			return "", e
		}
		if c.Lifecycle != "active" || action == "cancel" && !tx.Now().Before(c.UnlockAt) || action == "delete" && tx.Now().Before(c.UnlockAt) && c.ReviewState != "rejected" && !flags.Restricted && !flags.Privacy {
			return "", command.ErrConflict
		}
		terminal := "cancelled"
		if action == "delete" {
			terminal = "deleted"
		}
		if e = db.Model(&entity.WhisperCapsule{}).Where("id=?", id).Updates(map[string]any{"version": c.Version + 1, "lifecycle": terminal, "body_cipher": nil, "review_state": "cancelled", "review_task_id": nil}).Error; e != nil {
			return "", e
		}
		if e = db.Model(&entity.ModerationTask{}).Where("content_id=? AND status IN ('pending','machine_passed','human_required')", c.ContentRegistryID).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		// 人工理由可能引用原文；普通副本终结时移除原文引用；拒绝决定保留满足数据库约束的固定清理提示，独立证据域不在此范围。
		if e = db.Model(&entity.ModerationTask{}).Where("content_id=?", c.ContentRegistryID).Updates(map[string]any{"reason": gorm.Expr("CASE WHEN decision='rejected' THEN ? ELSE NULL END", "普通副本已清理"), "revision_suggestion": gorm.Expr("CASE WHEN decision='rejected' THEN ? ELSE NULL END", "普通副本已清理")}).Error; e != nil {
			return "", e
		}
		if e = db.Model(&entity.ContentRegistry{}).Where("id=?", c.ContentRegistryID).Update("status", "cancelled").Error; e != nil {
			return "", e
		}
		if e = cancelCapsuleWork(ctx, db, c, tx.Now()); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.capsule(ctx, tx.DB(), tx.Now(), ref, tx.Actor().ID, false)
		return e
	}
	_, err = s.execute(ctx, d, token, key, rid, struct{ Version int64 }{version}, 0, "", "maintenance", command.Expectation{Name: "expected_version", Target: target, Value: version})
	return
}
