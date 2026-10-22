package safety

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

func merge(a, b command.Discovery) command.Discovery {
	a.Accounts = append(a.Accounts, b.Accounts...)
	a.Admins = append(a.Admins, b.Admins...)
	a.Resources = append(a.Resources, b.Resources...)
	return a
}

// accountPlan 在账号锁前发现全部受影响关系；新增关系必须持同一账号锁，集合变化会触发框架重试。
func accountPlan(ctx context.Context, db *gorm.DB, accounts ...string) (p command.Discovery, err error) {
	db = db.WithContext(ctx)
	p.Accounts = append(p.Accounts, accounts...)
	var ids []string
	if err = db.Model(&entity.Wish{}).Where("owner_id IN ? OR id IN (SELECT wish_id FROM public.applications WHERE applicant_id IN ? AND status IN ('pending_scan','pending_review','active','accepted')) OR id IN (SELECT wish_id FROM public.matches WHERE (requester_id IN ? OR helper_id IN ?) AND status='current')", accounts, accounts, accounts, accounts).Pluck("id", &ids).Error; err != nil {
		return
	}
	for _, id := range ids {
		q, e := wish.DiscoverInteraction(ctx, db, id)
		if e != nil {
			return p, e
		}
		p = merge(p, q)
		var tasks []entity.ModerationTask
		if err = db.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.kind='story' AND c.entity_id=?", id).Find(&tasks).Error; err != nil {
			return
		}
		for _, t := range tasks {
			p.Resources = append(p.Resources, command.Resource{Kind: command.ModerationTask, ID: t.ID})
		}
	}
	return
}
func registry(db *gorm.DB, id string) (c entity.ContentRegistry, err error) {
	err = found(db.Where("id=?", id).Take(&c).Error)
	return
}
func targetRegistry(db *gorm.DB, kind, id string) (c entity.ContentRegistry, err error) {
	q := db.Where("kind=? AND entity_id=?", kind, id)
	switch kind {
	case "wish":
		q = q.Where("content_version=(SELECT current_content_version FROM public.wishes WHERE id=?)", id)
	case "story":
		q = q.Where("content_version=(SELECT current_story_version FROM public.wishes WHERE id=?)", id)
	case "application":
		q = q.Where("content_version=(SELECT content_version FROM public.applications WHERE id=?)", id)
	case "message":
		q = q.Where("content_version=(SELECT content_version FROM public.messages WHERE id=?)", id)
	default:
		return c, command.ErrInvalid
	}
	err = found(q.Take(&c).Error)
	return
}
func contentPlan(ctx context.Context, db *gorm.DB, c entity.ContentRegistry) (command.Discovery, error) {
	switch c.Kind {
	case "wish", "story":
		return accountContentPlan(ctx, db, c, c.EntityID)
	case "application":
		var a entity.Application
		if e := db.WithContext(ctx).Where("id=?", c.EntityID).Take(&a).Error; e != nil {
			return command.Discovery{}, found(e)
		}
		p, e := accountContentPlan(ctx, db, c, a.WishID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.Application, ID: a.ID})
		if a.ReviewTaskID != nil {
			p.Resources = append(p.Resources, command.Resource{Kind: command.ModerationTask, ID: *a.ReviewTaskID})
		}
		return p, e
	case "message":
		var m entity.Message
		if e := db.WithContext(ctx).Where("id=?", c.EntityID).Take(&m).Error; e != nil {
			return command.Discovery{}, found(e)
		}
		p, e := chat.DiscoverConversation(ctx, db, m.ConversationID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.Message, ID: m.ID})
		if m.ReviewTaskID != nil {
			p.Resources = append(p.Resources, command.Resource{Kind: command.ModerationTask, ID: *m.ReviewTaskID})
		}
		return p, e
	}
	return command.Discovery{}, command.ErrNotFound
}
func accountContentPlan(ctx context.Context, db *gorm.DB, c entity.ContentRegistry, id string) (command.Discovery, error) {
	p, e := wish.DiscoverInteraction(ctx, db, id)
	p.Accounts = append(p.Accounts, c.AuthorID)
	return p, e
}

// visible 只接受当前合法可见的内容，举报不能成为读取旧稿、未送达私聊或隔离正文的旁路。
func visible(ctx context.Context, db *gorm.DB, c entity.ContentRegistry, actor string, now time.Time) error {
	db = db.WithContext(ctx)
	var n int64
	if e := db.Model(&entity.Account{}).Where("id=? AND lifecycle='active'", c.AuthorID).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return command.ErrNotFound
	}
	if e := db.Model(&entity.ContentRestriction{}).Where("content_id=? AND active", c.ID).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrNotFound
	}
	if c.Status != "approved" {
		return command.ErrNotFound
	}
	current, e := targetRegistry(db, c.Kind, c.EntityID)
	if e != nil {
		return e
	}
	if current.ID != c.ID {
		return command.ErrNotFound
	}
	switch c.Kind {
	case "wish", "story":
		var w entity.Wish
		if e = db.Where("id=? AND draft_deleted_at IS NULL", c.EntityID).Take(&w).Error; e != nil {
			return found(e)
		}
		if c.Kind == "wish" {
			if w.State != "published" || w.ExpiresAt == nil || !now.Before(*w.ExpiresAt) {
				return command.ErrNotFound
			}
			if e = db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=? AND moderation_status='approved' AND public_body IS NOT NULL AND school_detection_state='completed'", w.ID, c.ContentVersion).Count(&n).Error; e != nil {
				return e
			}
		} else {
			if w.State != "fulfilled" {
				return command.ErrNotFound
			}
			if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=? AND moderation_status='approved' AND public_body IS NOT NULL AND withdrawn_at IS NULL AND school_detection_state='completed'", w.ID, c.ContentVersion).Count(&n).Error; e != nil {
				return e
			}
		}
		if n != 1 {
			return command.ErrNotFound
		}
		if e = db.Model(&entity.PrivacyHold{}).Where("target_id=? AND status='active'", w.ID).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return command.ErrNotFound
		}
		if e = db.Model(&entity.ContentRestriction{}).Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.entity_id=? AND c.kind IN ('wish','story') AND r.active", w.ID).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return command.ErrNotFound
		}
		if e = db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", c.AuthorID, now, now).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return command.ErrNotFound
		}
	case "application":
		var a entity.Application
		if e = db.Where("id=?", c.EntityID).Take(&a).Error; e != nil {
			return found(e)
		}
		var w entity.Wish
		if e = db.Where("id=?", a.WishID).Take(&w).Error; e != nil {
			return e
		}
		if actor != a.ApplicantID && actor != w.OwnerID {
			return command.ErrNotFound
		}
		ok, e := wish.ApplicationEligible(ctx, db, a, now)
		if e != nil {
			return e
		}
		if !ok || a.Status != "active" {
			return command.ErrNotFound
		}
	case "message":
		var m entity.Message
		if e = db.Where("id=? AND status='delivered'", c.EntityID).Take(&m).Error; e != nil {
			return found(e)
		}
		v, e := chat.LoadConversation(ctx, db, m.ConversationID)
		if e != nil {
			return e
		}
		if !v.CanRead(actor) {
			return command.ErrNotFound
		}
	}
	return nil
}

func penalty(db *gorm.DB, id string) (p entity.Penalty, err error) {
	err = found(db.Where("id=?", id).Take(&p).Error)
	return
}
func report(db *gorm.DB, id string) (p entity.Report, err error) {
	err = found(db.Where("id=?", id).Take(&p).Error)
	return
}
func appeal(db *gorm.DB, id string) (p entity.Appeal, err error) {
	err = found(db.Where("id=?", id).Take(&p).Error)
	return
}

// ordinaryCase 排除曾被扫描判为危机的内容。危机处置须使用独立案件模块，不能借普通举报解密。
func ordinaryCase(db *gorm.DB, cid string) error {
	var n int64
	if e := db.Model(&entity.ContentRestriction{}).Where("content_id=? AND kind='crisis' AND active", cid).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrNotFound
	}
	if e := db.Table("public.moderation_scans AS s").Joins("JOIN public.moderation_tasks t ON t.id=s.task_id").Where("t.content_id=? AND s.crisis_hit", cid).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrNotFound
	}
	return nil
}

// cancelRelationships 只取消尚未生效的交互。已有匹配保留，由当前限制实时暂停或原授权方例外退出。
func cancelRelationships(ctx context.Context, tx *command.Tx, a, b string) error {
	db := tx.DB().WithContext(ctx)
	var apps []entity.Application
	q := db.Table("public.applications AS a").Select("a.*").Joins("JOIN public.wishes w ON w.id=a.wish_id").Where("a.status IN ('pending_scan','pending_review','active')")
	if b == "" {
		q = q.Where("a.applicant_id=? OR w.owner_id=?", a, a)
	} else {
		q = q.Where("(a.applicant_id=? AND w.owner_id=?) OR (a.applicant_id=? AND w.owner_id=?)", a, b, b, a)
	}
	if e := q.Find(&apps).Error; e != nil {
		return e
	}
	for _, app := range apps {
		if e := wish.CancelApplication(ctx, db, app, "invalidated", "interaction_restricted"); e != nil {
			return e
		}
	}
	var cs []entity.Conversation
	cq := db.Table("public.conversations AS c").Select("c.*").Joins("JOIN public.matches m ON m.id=c.match_id").Where("m.status='current'")
	if b == "" {
		cq = cq.Where("m.requester_id=? OR m.helper_id=?", a, a)
	} else {
		cq = cq.Where("(m.requester_id=? AND m.helper_id=?) OR (m.requester_id=? AND m.helper_id=?)", a, b, b, a)
	}
	if e := cq.Find(&cs).Error; e != nil {
		return e
	}
	for _, c := range cs {
		if e := db.Model(&entity.Conversation{}).Where("id=?", c.ID).Updates(map[string]any{"write_epoch": gorm.Expr("write_epoch+1"), "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		var ms []entity.Message
		if e := db.Where("conversation_id=? AND status IN ('pending_scan','pending_review')", c.ID).Find(&ms).Error; e != nil {
			return e
		}
		for _, m := range ms {
			if e := chat.CancelMessage(ctx, db, m); e != nil {
				return e
			}
		}
	}
	return nil
}
