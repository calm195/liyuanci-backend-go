package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func loadWhisper(ctx context.Context, db *gorm.DB, taskID string) (r whisperSource, err error) {
	db = db.WithContext(ctx)
	if err = db.Where("id=?", taskID).Take(&r.task).Error; err != nil {
		return r, missingWhisper(err)
	}
	if err = db.Where(
		"id=? AND content_version=? AND kind IN ?",
		r.task.ContentID,
		r.task.ContentVersion,
		[]string{"whisper_post", "whisper_comment", "whisper_nickname", "whisper_capsule"},
	).Take(&r.registry).Error; err != nil {
		return r, missingWhisper(err)
	}
	task, registry := r.task, r.registry
	r.ID = r.registry.EntityID
	r.OwnerID = r.registry.AuthorID
	switch r.registry.Kind {
	case "whisper_post":
		err = db.Raw(`SELECT p.id,p.owner_id,p.version,p.current_content_version AS content_version,p.lifecycle,p.review_state AS state,p.publish_intent,p.privacy_epoch,p.restriction_epoch,p.review_round AS round,v.review_task_id AS task_id,v.identity_mode,v.nickname_version
 FROM public.whisper_posts p
 JOIN public.whisper_post_versions v ON v.post_id=p.id AND v.content_version=p.current_content_version
 WHERE p.id=?`, r.ID).Scan(&r).Error
	case "whisper_comment":
		err = db.Raw(`SELECT p.id,p.author_id AS owner_id,p.version,p.content_version,p.lifecycle,p.review_state AS state,p.privacy_epoch,p.restriction_epoch,p.review_round AS round,p.post_id AS parent_id,p.parent_content_version AS parent_version,p.comments_epoch,v.review_task_id AS task_id,v.identity_mode,v.nickname_version
 FROM public.whisper_comments p
 JOIN public.whisper_comment_versions v ON v.comment_id=p.id AND v.content_version=p.content_version
 WHERE p.id=?`, r.ID).Scan(&r).Error
	case "whisper_nickname":
		err = db.Raw(`SELECT content_registry_id AS id,account_id AS owner_id,control_version AS version,nickname_version AS content_version,'active' AS lifecycle,review_state AS state,review_round AS round,review_task_id AS task_id
 FROM public.whisper_nickname_versions
 WHERE content_registry_id=?`, r.registry.ID).Scan(&r).Error
		r.ID = registry.ID
	case "whisper_capsule":
		r.ContentVersion = 1
		err = db.Raw(`SELECT id,owner_id,version,lifecycle,review_state AS state,privacy_epoch,restriction_epoch,review_round AS round,review_task_id AS task_id,unlock_at
 FROM public.whisper_capsules
 WHERE id=?`, r.ID).Scan(&r).Error
	}
	r.task, r.registry = task, registry
	if registry.Kind == "whisper_capsule" {
		r.ContentVersion = registry.ContentVersion
	}
	if err != nil {
		return r, err
	}
	if r.Version == 0 {
		return r, command.ErrNotFound
	}
	err = db.Where("id=?", r.OwnerID).Take(&r.account).Error
	return r, err
}

func missingWhisper(e error) error {
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return command.ErrNotFound
	}
	return e
}

func whisperTarget(ctx context.Context, db *gorm.DB, kind, id string) (r whisperSource, err error) {
	db = db.WithContext(ctx)
	if !whisperKind(kind) || !validID(id) {
		return r, command.ErrNotFound
	}
	var t entity.ModerationTask
	q := db.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.kind=?", kind)
	if kind == "whisper_nickname" {
		q = q.Where("c.id=?", id)
	} else {
		q = q.Where("c.entity_id=?", id)
	}
	if err = q.Order("c.content_version DESC,t.created_at DESC,t.id DESC").Take(&t).Error; err != nil {
		return r, missingWhisper(err)
	}
	return loadWhisper(ctx, db, t.ID)
}

// countWhisperBarriers 汇总本次审核必须重新确认的处罚、内容和隐私屏障。
func countWhisperBarriers(ctx context.Context, db *gorm.DB, r whisperSource) (n int64, err error) {
	err = db.WithContext(ctx).Raw(`SELECT (SELECT count(*)
 FROM public.penalties
 WHERE ?<>'whisper_capsule' AND account_id=? AND revoked_at IS NULL AND action_kind IN ('mute','ban') AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp()))+(SELECT count(*)
 FROM public.content_restrictions x
 JOIN public.content_registry c ON c.id=x.content_id
 WHERE x.active AND c.kind=? AND (c.entity_id=? OR c.id=?))+(SELECT count(*)
 FROM public.privacy_holds
 WHERE target_kind=? AND target_id=? AND (status='active' OR hold_epoch>?))`, r.registry.Kind, r.OwnerID, r.registry.Kind, r.registry.EntityID, r.ID, r.registry.Kind, r.ID, r.task.HoldEpoch).
		Scan(&n).
		Error
	return
}

// countWhisperNicknameReferences 核对候选或已批准昵称仍被当前偏好引用。
func countWhisperNicknameReferences(ctx context.Context, db *gorm.DB, r whisperSource) (n int64, err error) {
	err = db.WithContext(ctx).Raw(`SELECT count(*)
 FROM public.whisper_preferences
 WHERE account_id=? AND (candidate_nickname_version=? OR active_nickname_version=? AND ?='approved')`, r.OwnerID, r.ContentVersion, r.ContentVersion, r.State).
		Scan(&n).
		Error
	return
}

// countApprovedWhisperNickname 核对当前身份快照仍获批准且未被限制。
func countApprovedWhisperNickname(ctx context.Context, db *gorm.DB, r whisperSource) (n int64, err error) {
	err = db.WithContext(ctx).Raw(`SELECT count(*)
 FROM public.whisper_nickname_versions n
 WHERE account_id=? AND nickname_version=? AND review_state='approved' AND revoked_at IS NULL AND NOT EXISTS(SELECT 1
 FROM public.content_restrictions x
 WHERE x.content_id=n.content_registry_id AND x.active)`, r.OwnerID, *r.NicknameVersion).Scan(&n).Error
	return
}

// countCommentableWhisperParent 在原事务锁内复核父帖版本、评论开关和双向拉黑。
func countCommentableWhisperParent(ctx context.Context, db *gorm.DB, r whisperSource) (n int64, err error) {
	err = db.WithContext(ctx).Raw(`SELECT count(*)
 FROM public.whisper_posts p
 JOIN public.accounts a ON a.id=p.owner_id
 JOIN public.whisper_post_versions v ON v.post_id=p.id AND v.content_version=p.current_content_version
 WHERE p.id=? AND p.lifecycle='active' AND p.publish_intent AND p.comments_open
 AND p.comments_epoch=? AND p.published_content_version=? AND p.current_content_version=?
 AND v.review_state='approved' AND a.lifecycle='active' AND a.verification='verified' AND NOT EXISTS(SELECT 1
 FROM public.blocks
 WHERE (owner_id=? AND target_id=a.id) OR (owner_id=a.id AND target_id=?))
 AND NOT EXISTS(SELECT 1
 FROM public.penalties
 WHERE account_id=a.id AND revoked_at IS NULL AND action_kind IN ('mute','ban')
 AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp()))
 AND NOT EXISTS(SELECT 1
 FROM public.content_restrictions x
 JOIN public.content_registry c ON c.id=x.content_id
 WHERE x.active AND c.kind='whisper_post' AND c.entity_id=p.id)
 AND NOT EXISTS(SELECT 1
 FROM public.privacy_holds
 WHERE target_kind='whisper_post' AND target_id=p.id AND status='active')`, *r.ParentID, r.CommentsEpoch, r.ParentVersion, r.ParentVersion, r.OwnerID, r.OwnerID).
		Scan(&n).
		Error
	return
}

type whisperCipher struct{ Body, Identity json.RawMessage }

// readWhisperCipher 只供已持有审核资源锁的内部路径读取当前密文，不生成公开投影。
func readWhisperCipher(ctx context.Context, db *gorm.DB, r whisperSource) (raw whisperCipher, err error) {
	db = db.WithContext(ctx)
	switch r.registry.Kind {
	case "whisper_post":
		err = db.Raw("SELECT body_cipher AS body,identity_cipher AS identity FROM public.whisper_post_versions WHERE content_registry_id=?", r.registry.ID).
			Scan(&raw).
			Error
	case "whisper_comment":
		err = db.Raw("SELECT body_cipher AS body,identity_cipher AS identity FROM public.whisper_comment_versions WHERE content_registry_id=?", r.registry.ID).
			Scan(&raw).
			Error
	case "whisper_nickname":
		err = db.Raw("SELECT label_cipher AS body FROM public.whisper_nickname_versions WHERE content_registry_id=?", r.registry.ID).Scan(&raw).Error
	case "whisper_capsule":
		err = db.Raw("SELECT body_cipher AS body FROM public.whisper_capsules WHERE id=?", r.ID).Scan(&raw).Error
	}
	return
}

type whisperControlFlags struct{ Restricted, Isolated, Privacy bool }

func readWhisperControlFlags(ctx context.Context, db *gorm.DB, r whisperSource) (flags whisperControlFlags, err error) {
	err = db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1
 FROM public.content_restrictions x
 JOIN public.content_registry c ON c.id=x.content_id
 WHERE c.kind=? AND c.entity_id=? AND x.active) AS restricted,EXISTS(SELECT 1
 FROM public.content_restrictions x
 JOIN public.content_registry c ON c.id=x.content_id
 WHERE c.kind=? AND c.entity_id=? AND x.active AND x.kind='crisis') AS isolated,EXISTS(SELECT 1
 FROM public.privacy_holds
 WHERE target_kind=? AND target_id=? AND status='active') AS privacy`, r.registry.Kind, r.registry.EntityID, r.registry.Kind, r.registry.EntityID, r.registry.Kind, r.ID).
		Scan(&flags).
		Error
	return
}

func whisperUnlocked(ctx context.Context, db *gorm.DB, unlockAt time.Time) (unlocked bool, err error) {
	err = db.WithContext(ctx).Raw("SELECT ?<=clock_timestamp()", unlockAt).Scan(&unlocked).Error
	return
}

// releaseWhisperRestrictions 只解除普通违规屏障，危机与隐私屏障保持独立。
func releaseWhisperRestrictions(ctx context.Context, db *gorm.DB, kind, entityID string) error {
	return db.WithContext(ctx).Exec(`UPDATE public.content_restrictions x
 SET active=false, version=x.version+1
 FROM public.content_registry c
 WHERE c.id=x.content_id AND c.kind=? AND c.entity_id=?
 AND x.kind='violation' AND x.active`, kind, entityID).Error
}
