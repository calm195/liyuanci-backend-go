package privacy

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// whisperPlan 先发现整串幸存作者与父资源，再由上层按统一顺序取锁重验。
// 评论创建、审核和解锁都持同一作者/帖主guard，避免注销后追加遗漏对象。
func whisperPlan(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	db = db.WithContext(ctx)
	var posts []entity.WhisperPost
	if err = db.Where("owner_id=? OR id IN (SELECT post_id FROM public.whisper_comments WHERE author_id=?)", id, id).Find(&posts).Error; err != nil {
		return
	}
	for _, v := range posts {
		p.Accounts = append(p.Accounts, v.OwnerID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperPost, ID: v.ID})
	}
	var comments []entity.WhisperComment
	if err = db.Where("author_id=? OR post_id IN (SELECT id FROM public.whisper_posts WHERE owner_id=?)", id, id).Find(&comments).Error; err != nil {
		return
	}
	for _, v := range comments {
		p.Accounts = append(p.Accounts, v.AuthorID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperComment, ID: v.ID})
	}
	var capsules []entity.WhisperCapsule
	if err = db.Where("owner_id=?", id).Find(&capsules).Error; err != nil {
		return
	}
	for _, v := range capsules {
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperCapsule, ID: v.ID})
	}
	var names []entity.WhisperNicknameVersion
	if err = db.Where("account_id=?", id).Find(&names).Error; err != nil {
		return
	}
	for _, v := range names {
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperNickname, ID: v.ContentRegistryID})
	}
	return
}

func collectWhispers(db *gorm.DB, id string, g *cleanGraph) error {
	queries := []struct {
		dst *[]string
		sql string
	}{
		{&g.posts, "SELECT id FROM public.whisper_posts WHERE owner_id=?"},
		{&g.comments, "SELECT id FROM public.whisper_comments WHERE author_id=?"},
		{&g.capsules, "SELECT id FROM public.whisper_capsules WHERE owner_id=?"},
		{&g.nicknames, "SELECT content_registry_id FROM public.whisper_nickname_versions WHERE account_id=?"},
		{&g.relatedComments, "SELECT id FROM public.whisper_comments WHERE author_id<>? AND post_id IN (SELECT id FROM public.whisper_posts WHERE owner_id=?)"},
	}
	for _, q := range queries {
		if e := execSubjectScan(db, q.sql, id, q.dst); e != nil {
			return e
		}
	}
	var blockRefs []string
	if e := db.Model(&entity.Block{}).Where("owner_id=? OR target_id=?", id, id).Pluck("block_id", &blockRefs).Error; e != nil {
		return e
	}
	g.refs = append(g.refs, blockRefs...)
	// 幸存评论本体与hold保留，旧审核片段、投递和回执则纳入普通副本清理。
	var tasks []string
	if e := db.Raw("SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry r ON r.id=t.content_id WHERE r.kind='whisper_comment' AND r.entity_id IN ?", g.relatedComments).Scan(&tasks).Error; e != nil {
		return e
	}
	g.refs = append(g.refs, g.relatedComments...)
	g.refs = append(g.refs, tasks...)
	return nil
}
func execSubjectScan(db *gorm.DB, sql, id string, dst any) error {
	args := []any{}
	for _, c := range sql {
		if c == '?' {
			args = append(args, id)
		}
	}
	return db.Raw(sql, args...).Scan(dst).Error
}

// activateWhispers 同受理事务建立整串屏障，保留受控脱敏所需密文但立即清空公开候选。
// 不删除独立证据；缺少外部清理或脱敏适配器时这些屏障持续有效。
func activateWhispers(ctx context.Context, db *gorm.DB, id string) error {
	db = db.WithContext(ctx)
	var g cleanGraph
	if e := collectWhispers(db, id, &g); e != nil {
		return e
	}
	var comments []entity.WhisperComment
	if e := db.Where("author_id=? OR id IN ?", id, g.relatedComments).Find(&comments).Error; e != nil {
		return e
	}
	for _, v := range comments {
		if e := ensureWhisperHold(db, "whisper_comment", v.ID, v.ContentVersion+1); e != nil {
			return e
		}
	}
	for _, v := range g.posts {
		if e := ensureWhisperHold(db, "whisper_post", v, 1); e != nil {
			return e
		}
	}
	for _, v := range g.capsules {
		if e := ensureWhisperHold(db, "whisper_capsule", v, 1); e != nil {
			return e
		}
	}
	for _, v := range g.nicknames {
		if e := ensureWhisperHold(db, "whisper_nickname", v, 1); e != nil {
			return e
		}
	}
	allComments := append(append([]string{}, g.comments...), g.relatedComments...)
	updates := []struct {
		sql  string
		args []any
	}{
		{"UPDATE public.whisper_posts SET version=version+CASE WHEN privacy_epoch=0 THEN 1 ELSE 0 END,privacy_epoch=GREATEST(privacy_epoch,1),published_content_version=NULL,publish_intent=false,comments_epoch=comments_epoch+1 WHERE owner_id=?", []any{id}},
		{"UPDATE public.whisper_post_versions SET public_body=NULL,public_identity=NULL,candidate_cipher=NULL WHERE post_id IN ?", []any{g.posts}},
		{"UPDATE public.whisper_comments SET version=version+CASE WHEN privacy_epoch=0 THEN 1 ELSE 0 END,privacy_epoch=GREATEST(privacy_epoch,1),review_state='cancelled',recovery_cipher=NULL WHERE id IN ?", []any{allComments}},
		{"UPDATE public.whisper_comment_versions SET public_body=NULL,public_identity=NULL,candidate_cipher=NULL,review_result_cipher=NULL WHERE comment_id IN ?", []any{allComments}},
		{"UPDATE public.whisper_nickname_versions SET public_label=NULL,revoked_at=clock_timestamp() WHERE account_id=?", []any{id}},
		{"UPDATE public.whisper_capsules SET version=version+CASE WHEN privacy_epoch=0 THEN 1 ELSE 0 END,privacy_epoch=GREATEST(privacy_epoch,1),lifecycle='cancelled',body_cipher=NULL WHERE owner_id=?", []any{id}},
		{"UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE content_id IN (SELECT id FROM public.content_registry WHERE author_id=? OR (kind='whisper_comment' AND entity_id IN ?)) AND status IN ('pending','machine_passed','human_required')", []any{id, g.relatedComments}},
		{"UPDATE public.work_items SET status='cancelled',lease_until=NULL WHERE (target_id IN ? OR target_id IN (SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry r ON r.id=t.content_id WHERE r.author_id=? OR (r.kind='whisper_comment' AND r.entity_id IN ?))) AND status IN ('pending','leased','waiting','retry_wait','attention')", []any{g.capsules, id, g.relatedComments}},
		{"UPDATE public.outbox_events SET superseded_at=COALESCE(superseded_at,clock_timestamp()),lease_until=NULL WHERE work_id IN (SELECT id FROM public.work_items WHERE status='cancelled' AND (target_id IN ? OR target_id IN (SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry r ON r.id=t.content_id WHERE r.author_id=? OR (r.kind='whisper_comment' AND r.entity_id IN ?)))) AND superseded_at IS NULL", []any{g.capsules, id, g.relatedComments}},
	}
	for _, q := range updates {
		if e := db.Exec(q.sql, q.args...).Error; e != nil {
			return e
		}
	}
	// 先撤销树洞待投递通知，避免暂停清理期间仍发出旧父帖引用。
	refs := append(append(append([]string{}, g.posts...), allComments...), g.capsules...)
	var notices []string
	if e := db.Model(&entity.WhisperNotificationRef{}).Where("source_id IN ? OR target_id IN ? OR comment_id IN ?", refs, refs, allComments).Pluck("notification_id", &notices).Error; e != nil {
		return e
	}
	// 同步抑制读取与投递，但保留技术定位给外部缓存清理；物理行由最终图删除。
	return db.Model(&entity.WhisperNotificationRef{}).Where("notification_id IN ?", notices).Update("suppressed", true).Error
}

type whisperRedaction struct {
	comment entity.WhisperComment
	body    string
}

func (s *Service) prepareWhisperRedactions(ctx context.Context, in Intent) (out []whisperRedaction, err error) {
	if in.Scope != "account" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	var rows []entity.WhisperComment
	if err = s.db.WithContext(ctx).Where("author_id<>? AND post_id IN (SELECT id FROM public.whisper_posts WHERE owner_id=?)", in.Target, in.Target).Find(&rows).Error; err != nil {
		return
	}
	for _, c := range rows {
		var v entity.WhisperCommentVersion
		if err = s.db.WithContext(ctx).Where("comment_id=? AND content_version=?", c.ID, c.ContentVersion).Take(&v).Error; err != nil {
			return
		}
		var body string
		if err = s.decrypt(ctx, "whisper_comment_body", c.ID, c.ContentVersion, v.BodyCipher, &body); err != nil {
			return
		}
		body, err = s.redact(ctx, "whisper_comment", body)
		if err != nil {
			return
		}
		out = append(out, whisperRedaction{c, body})
	}
	return
}

// applyWhisperRedactions 只存受控安全候选，擦除所有历史原文、身份与审核片段。
// 版本再次验证防止外部脱敏期间作者撤回或内容变化被旧结果覆盖；不重新公开旧串。
func (s *Service) applyWhisperRedactions(ctx context.Context, db *gorm.DB, rows []whisperRedaction) error {
	for _, r := range rows {
		var c entity.WhisperComment
		if e := db.Where("id=?", r.comment.ID).Take(&c).Error; e != nil {
			return e
		}
		if c.Version != r.comment.Version {
			return command.ErrRetry
		}
		candidate, e := s.encrypt(ctx, "whisper_comment_recovery", c.ID, c.ContentVersion, r.body)
		if e != nil {
			return e
		}
		var versions []entity.WhisperCommentVersion
		if e = db.Where("comment_id=?", c.ID).Find(&versions).Error; e != nil {
			return e
		}
		for _, v := range versions {
			raw, e := s.encrypt(ctx, "whisper_comment_body", c.ID, v.ContentVersion, r.body)
			if e != nil {
				return e
			}
			identity, e := s.encrypt(ctx, "whisper_comment_identity", c.ID, v.ContentVersion, map[string]string{"mode": "anonymous"})
			if e != nil {
				return e
			}
			if e = db.Model(&entity.WhisperCommentVersion{}).Where("comment_id=? AND content_version=?", c.ID, v.ContentVersion).Updates(map[string]any{"body_cipher": raw, "identity_cipher": identity, "nickname_version": nil, "public_body": nil, "public_identity": nil, "candidate_cipher": nil, "review_result_cipher": nil}).Error; e != nil {
				return e
			}
		}
		if e = db.Exec("DELETE FROM public.moderation_scans WHERE task_id IN (SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry r ON r.id=t.content_id WHERE r.kind='whisper_comment' AND r.entity_id=?)", c.ID).Error; e != nil {
			return e
		}
		if e = db.Exec("UPDATE public.moderation_tasks SET reason=NULL,revision_suggestion=NULL,decision=NULL WHERE content_id IN (SELECT id FROM public.content_registry WHERE kind='whisper_comment' AND entity_id=?)", c.ID).Error; e != nil {
			return e
		}
		if c.Lifecycle != "active" {
			candidate = nil
		}
		if e = db.Model(&entity.WhisperComment{}).Where("id=?", c.ID).Updates(map[string]any{"recovery_cipher": candidate, "version": gorm.Expr("version+1"), "updated_at": time.Now().UTC()}).Error; e != nil {
			return e
		}
	}
	return nil
}

// ensureWhisperHold 保留已有保护代次，重复恢复不使脱敏版本持续变化。
func ensureWhisperHold(db *gorm.DB, kind, id string, version int64) error {
	var n int64
	if e := db.Model(&entity.PrivacyHold{}).Where("target_kind=? AND target_id=? AND status='active'", kind, id).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	return hold(db, kind, id, version)
}
