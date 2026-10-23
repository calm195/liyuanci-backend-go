package privacy

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

func merge(p, q command.Discovery) command.Discovery {
	p.Accounts = append(p.Accounts, q.Accounts...)
	p.Admins = append(p.Admins, q.Admins...)
	p.Resources = append(p.Resources, q.Resources...)
	return p
}

// accountPlan 覆盖历史及当前关系，清理双方聊天时也锁定原双方；集合变化整单重试。
func accountPlan(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	db = db.WithContext(ctx)
	p.Accounts = []string{id}
	var wishes []string
	if err = db.Model(&entity.Wish{}).Where("owner_id=? OR id IN (SELECT wish_id FROM public.applications WHERE applicant_id=?) OR id IN (SELECT wish_id FROM public.matches WHERE requester_id=? OR helper_id=?)", id, id, id, id).Pluck("id", &wishes).Error; err != nil {
		return
	}
	for _, wid := range wishes {
		q, e := wish.DiscoverStory(ctx, db, wid)
		if e != nil {
			return p, e
		}
		p = merge(p, q)
		var matches []entity.Match
		if err = db.Where("wish_id=?", wid).Find(&matches).Error; err != nil {
			return
		}
		for _, m := range matches {
			p.Accounts = append(p.Accounts, m.RequesterID, m.HelperID)
			p.Resources = append(p.Resources, command.Resource{Kind: command.Match, ID: m.ID})
		}
	}
	// 全部私有资产、认证和审核也须遵循统一锁序，不能只锁仍待审任务。
	for _, spec := range []struct {
		kind command.ResourceKind
		sql  string
	}{
		{command.Asset, "SELECT id FROM public.assets WHERE owner_id=?"},
		{command.IdentityReview, "SELECT id FROM public.identity_reviews WHERE account_id=?"},
		{command.ModerationTask, `SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry c ON c.id=t.content_id WHERE c.author_id=? OR c.entity_id IN (SELECT id FROM public.wishes WHERE owner_id=?) OR c.entity_id IN (SELECT wish_id FROM public.matches WHERE helper_id=?)`},
	} {
		var ids []string
		args := []any{id}
		if spec.kind == command.ModerationTask {
			args = []any{id, id, id}
		}
		if err = db.Raw(spec.sql, args...).Scan(&ids).Error; err != nil {
			return
		}
		for _, v := range ids {
			p.Resources = append(p.Resources, command.Resource{Kind: spec.kind, ID: v})
		}
	}
	return command.NormalizePlan(p)
}

func evidencePlan(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	p.Resources = []command.Resource{{Kind: command.EvidenceCase, ID: id}}
	var rows []entity.EvidenceCrisisCase
	err = db.WithContext(ctx).Where("evidence_case_id=?", id).Find(&rows).Error
	for _, c := range rows {
		p.Resources = append(p.Resources, command.Resource{Kind: command.CrisisCase, ID: c.ID})
	}
	return
}

func equalPlan(a, b command.Discovery) bool {
	a, e := command.NormalizePlan(a)
	b, f := command.NormalizePlan(b)
	return e == nil && f == nil && reflect.DeepEqual(a, b)
}

func hold(db *gorm.DB, kind, id string, required int64) error {
	var epoch int64
	if e := db.Model(&entity.PrivacyHold{}).Select("coalesce(max(hold_epoch),0)+1").Where("target_kind=? AND target_id=?", kind, id).Scan(&epoch).Error; e != nil {
		return e
	}
	return db.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: kind, TargetID: id, HoldEpoch: epoch, RequiredContentVersion: required, Status: "active"}).Error
}

// activateAccount 与deleting同提交全部屏障和六行矩阵，不依赖清理消费者是否在线。
func activateAccount(ctx context.Context, tx *command.Tx, id string) error {
	db := tx.DB().WithContext(ctx)
	var a entity.Account
	if e := db.Where("id=?", id).Take(&a).Error; e != nil {
		return e
	}
	if a.Lifecycle == "deleting" {
		return nil
	}
	if e := db.Model(&entity.Account{}).Where("id=?", id).Updates(map[string]any{"lifecycle": "deleting", "auth_epoch": gorm.Expr("auth_epoch+1"), "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.Session{}).Where("account_id=?", id).Update("revoked_at", tx.Now()).Error; e != nil {
		return e
	}
	var rows []entity.Wish
	if e := db.Where("owner_id=? OR id IN (SELECT wish_id FROM public.matches WHERE helper_id=?)", id, id).Find(&rows).Error; e != nil {
		return e
	}
	for _, w := range rows {
		if e := hold(db, "wish", w.ID, w.CurrentContentVersion+1); e != nil {
			return e
		}
		if w.CurrentStoryVersion > 0 {
			if e := hold(db, "story", w.ID, w.CurrentStoryVersion+1); e != nil {
				return e
			}
		}
		updates := map[string]any{"version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1"), "refresh_on_publish": false}
		if w.OwnerID == id {
			updates["draft_deleted_at"] = tx.Now()
			if w.State != "fulfilled" {
				updates["state"] = "withdrawn"
			}
		} else {
			updates["peer_deleted"] = true
			var current int64
			if e := db.Model(&entity.Match{}).Where("wish_id=? AND helper_id=? AND status='current'", w.ID, id).Count(&current).Error; e != nil {
				return e
			}
			if current > 0 && (w.State == "matching" || w.State == "realized") {
				updates["state"] = "withdrawn"
			}
		}
		if e := db.Model(&entity.Wish{}).Where("id=?", w.ID).Updates(updates).Error; e != nil {
			return e
		}
		// 所有历史公开候选与身份标签立即失效；原稿只有本人隐私重审路径能处理。
		if e := db.Model(&entity.WishVersion{}).Where("wish_id=?", w.ID).Updates(map[string]any{"public_body": nil, "identity_tags": emptyJSON(), "moderation_status": "obsolete"}).Error; e != nil {
			return e
		}
		if e := db.Model(&entity.StoryVersion{}).Where("wish_id=?", w.ID).Updates(map[string]any{"public_body": nil, "identity_tags": emptyJSON(), "moderation_status": "obsolete"}).Error; e != nil {
			return e
		}
	}
	// 取消全部相关普通内容任务和投递；已送达序号不改写，双方读取由主体生命周期拒绝。
	statements := []string{
		`UPDATE public.identity_reviews SET status='cancelled',version=version+1 WHERE account_id=? AND status='pending'`,
		`UPDATE public.assets SET status='deleting',version=version+1 WHERE owner_id=?`,
		`UPDATE public.applications SET status='invalidated',public_message_cipher=NULL,cancel_reason='account_deleted',version=version+1 WHERE applicant_id=? OR wish_id IN (SELECT id FROM public.wishes WHERE owner_id=?) OR (status IN ('pending_scan','pending_review','active') AND wish_id IN (SELECT wish_id FROM public.matches WHERE helper_id=?))`,
		`UPDATE public.messages SET status='cancelled',version=version+1 WHERE status IN ('pending_scan','pending_review') AND conversation_id IN (SELECT c.id FROM public.conversations c JOIN public.matches m ON m.id=c.match_id WHERE m.requester_id=? OR m.helper_id=?)`,
		`UPDATE public.conversations SET write_epoch=write_epoch+1,version=version+1 WHERE match_id IN (SELECT id FROM public.matches WHERE requester_id=? OR helper_id=?)`,
		`UPDATE public.matches SET status='ended',ended_at=clock_timestamp(),version=version+1 WHERE status='current' AND (requester_id=? OR helper_id=?)`,
	}
	for _, sql := range statements {
		if e := execSubject(db, sql, id); e != nil {
			return e
		}
	}
	var contents []string
	sql := `SELECT id FROM public.content_registry WHERE author_id=?
 OR (kind IN ('wish','story') AND entity_id IN (SELECT id FROM public.wishes WHERE owner_id=? OR id IN (SELECT wish_id FROM public.matches WHERE helper_id=?)))
 OR (kind='application' AND entity_id IN (SELECT id FROM public.applications WHERE applicant_id=? OR cancel_reason='account_deleted' AND wish_id IN (SELECT wish_id FROM public.matches WHERE helper_id=?) OR wish_id IN (SELECT id FROM public.wishes WHERE owner_id=?)))
 OR (kind='message' AND entity_id IN (SELECT msg.id FROM public.messages msg JOIN public.conversations c ON c.id=msg.conversation_id JOIN public.matches m ON m.id=c.match_id WHERE m.requester_id=? OR m.helper_id=?))`
	if e := db.Raw(sql, id, id, id, id, id, id, id, id).Scan(&contents).Error; e != nil {
		return e
	}
	var tasks, assets, works []string
	if e := db.Model(&entity.ModerationTask{}).Where("content_id IN ?", contents).Pluck("id", &tasks).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.ModerationTask{}).Where("id IN ?", tasks).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.ContentRegistry{}).Where("id IN ?", contents).Update("status", "cancelled").Error; e != nil {
		return e
	}
	if e := db.Model(&entity.Asset{}).Where("owner_id=?", id).Pluck("id", &assets).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.WorkItem{}).Where("(target_kind='moderation_task' AND target_id IN ?) OR (target_kind='asset' AND target_id IN ?)", tasks, assets).Pluck("id", &works).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.WorkItem{}).Where("id IN ?", works).Updates(map[string]any{"status": "cancelled", "lease_until": nil, "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.OutboxEvent{}).Where("work_id IN ?", works).Updates(map[string]any{"superseded_at": tx.Now(), "lease_until": nil}).Error; e != nil {
		return e
	}
	return nil
}
func emptyJSON() json.RawMessage { return json.RawMessage("{}") }

func execSubject(db *gorm.DB, sql, id string) error {
	args := make([]any, 0)
	for _, c := range sql {
		if c == '?' {
			args = append(args, id)
		}
	}
	return db.Exec(sql, args...).Error
}

// activateEvidence 先阻断读取和投递，再由清理阶段永久清除；不触及原普通内容隔离。
func activateEvidence(ctx context.Context, db *gorm.DB, id string) error {
	db = db.WithContext(ctx)
	if e := db.Model(&entity.EvidenceCase{}).Where("id=? AND status<>'deletion_pending'", id).Updates(map[string]any{"status": "deletion_pending", "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	return db.Model(&entity.CrisisDelivery{}).Where("case_id IN (SELECT id FROM evidence.crisis_cases WHERE evidence_case_id=?) AND status IN ('pending','leased')", id).Updates(map[string]any{"status": "cancelled", "lease_id": nil, "lease_until": nil}).Error
}
