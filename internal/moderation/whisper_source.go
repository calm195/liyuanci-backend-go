package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// whisperSource 只装载治理元数据；原稿必须经受限投影或专用工作身份读取。
type whisperSource struct {
	task                                                           entity.ModerationTask
	registry                                                       entity.ContentRegistry
	account                                                        entity.Account
	ID, OwnerID, State, Lifecycle, IdentityMode                    string
	Version, ContentVersion, PrivacyEpoch, RestrictionEpoch, Round int64
	TaskID                                                         *string
	NicknameVersion                                                *int64
	ParentID                                                       *string
	ParentVersion, CommentsEpoch                                   int64
	PublishIntent                                                  bool
	UnlockAt                                                       time.Time
}

func whisperKind(k string) bool {
	return k == "whisper_post" || k == "whisper_comment" || k == "whisper_nickname" || k == "whisper_capsule"
}

func loadWhisper(db *gorm.DB, taskID string) (r whisperSource, err error) {
	if err = db.Where("id=?", taskID).Take(&r.task).Error; err != nil {
		return r, missingWhisper(err)
	}
	if err = db.Where("id=? AND content_version=? AND kind IN ?", r.task.ContentID, r.task.ContentVersion, []string{"whisper_post", "whisper_comment", "whisper_nickname", "whisper_capsule"}).Take(&r.registry).Error; err != nil {
		return r, missingWhisper(err)
	}
	task, registry := r.task, r.registry
	r.ID = r.registry.EntityID
	r.OwnerID = r.registry.AuthorID
	switch r.registry.Kind {
	case "whisper_post":
		err = db.Raw(`SELECT p.id,p.owner_id,p.version,p.current_content_version AS content_version,p.lifecycle,p.review_state AS state,p.publish_intent,p.privacy_epoch,p.restriction_epoch,p.review_round AS round,v.review_task_id AS task_id,v.identity_mode,v.nickname_version FROM public.whisper_posts p JOIN public.whisper_post_versions v ON v.post_id=p.id AND v.content_version=p.current_content_version WHERE p.id=?`, r.ID).Scan(&r).Error
	case "whisper_comment":
		err = db.Raw(`SELECT p.id,p.author_id AS owner_id,p.version,p.content_version,p.lifecycle,p.review_state AS state,p.privacy_epoch,p.restriction_epoch,p.review_round AS round,p.post_id AS parent_id,p.parent_content_version AS parent_version,p.comments_epoch,v.review_task_id AS task_id,v.identity_mode,v.nickname_version FROM public.whisper_comments p JOIN public.whisper_comment_versions v ON v.comment_id=p.id AND v.content_version=p.content_version WHERE p.id=?`, r.ID).Scan(&r).Error
	case "whisper_nickname":
		err = db.Raw(`SELECT content_registry_id AS id,account_id AS owner_id,control_version AS version,nickname_version AS content_version,'active' AS lifecycle,review_state AS state,review_round AS round,review_task_id AS task_id FROM public.whisper_nickname_versions WHERE content_registry_id=?`, r.registry.ID).Scan(&r).Error
		r.ID = registry.ID
	case "whisper_capsule":
		r.ContentVersion = 1
		err = db.Raw(`SELECT id,owner_id,version,lifecycle,review_state AS state,privacy_epoch,restriction_epoch,review_round AS round,review_task_id AS task_id,unlock_at FROM public.whisper_capsules WHERE id=?`, r.ID).Scan(&r).Error
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
func whisperPlan(ctx context.Context, db *gorm.DB, id string) (command.Discovery, error) {
	r, e := loadWhisper(db.WithContext(ctx), id)
	if errors.Is(e, command.ErrNotFound) {
		return command.Discovery{}, nil
	}
	if e != nil {
		return command.Discovery{}, e
	}
	kind := map[string]command.ResourceKind{"whisper_post": command.WhisperPost, "whisper_comment": command.WhisperComment, "whisper_nickname": command.WhisperNickname, "whisper_capsule": command.WhisperCapsule}[r.registry.Kind]
	p := command.Discovery{Accounts: []string{r.OwnerID}, Resources: []command.Resource{{Kind: kind, ID: r.ID}, {Kind: command.ModerationTask, ID: id}}}
	if r.ParentID != nil {
		var parent entity.WhisperPost
		if e = db.WithContext(ctx).Select("id,owner_id").Where("id=?", *r.ParentID).Take(&parent).Error; e != nil {
			return p, e
		}
		p.Accounts = append(p.Accounts, parent.OwnerID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperPost, ID: parent.ID})
	}
	// 昵称的撤销与公开决定共享同一资源锁，避免批准后立即泄漏旧快照。
	if r.NicknameVersion != nil {
		var n entity.WhisperNicknameVersion
		if e = db.WithContext(ctx).Select("content_registry_id").Where("account_id=? AND nickname_version=?", r.OwnerID, *r.NicknameVersion).Take(&n).Error; e != nil {
			return p, e
		}
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperNickname, ID: n.ContentRegistryID})
	}
	return p, nil
}
func whisperCurrent(r whisperSource) bool {
	return r.TaskID != nil && *r.TaskID == r.task.ID && r.ContentVersion == r.task.ContentVersion && r.Lifecycle == "active" && r.account.Lifecycle == "active" && r.account.AuthEpoch == r.task.SubjectEpoch
}
func whisperEligible(db *gorm.DB, r whisperSource) (bool, error) {
	if !whisperCurrent(r) || r.task.HoldEpoch != r.PrivacyEpoch || r.task.RestrictionEpoch == nil || *r.task.RestrictionEpoch != r.RestrictionEpoch {
		return false, nil
	}
	if (r.registry.Kind == "whisper_post" || r.registry.Kind == "whisper_comment") && r.account.Verification != "verified" {
		return false, nil
	}
	if r.registry.Kind == "whisper_post" && !r.PublishIntent {
		return false, nil
	}
	var n int64
	if e := db.Raw(`SELECT (SELECT count(*) FROM public.penalties WHERE ?<>'whisper_capsule' AND account_id=? AND revoked_at IS NULL AND action_kind IN ('mute','ban') AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp()))+(SELECT count(*) FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE x.active AND c.kind=? AND (c.entity_id=? OR c.id=?))+(SELECT count(*) FROM public.privacy_holds WHERE target_kind=? AND target_id=? AND (status='active' OR hold_epoch>?))`, r.registry.Kind, r.OwnerID, r.registry.Kind, r.registry.EntityID, r.ID, r.registry.Kind, r.ID, r.task.HoldEpoch).Scan(&n).Error; e != nil {
		return false, e
	}
	if n != 0 {
		return false, nil
	}
	if r.registry.Kind == "whisper_nickname" {
		if e := db.Raw(`SELECT count(*) FROM public.whisper_preferences WHERE account_id=? AND (candidate_nickname_version=? OR active_nickname_version=? AND ?='approved')`, r.OwnerID, r.ContentVersion, r.ContentVersion, r.State).Scan(&n).Error; e != nil {
			return false, e
		}
		return n == 1, nil
	}
	if r.NicknameVersion != nil {
		if e := db.Raw(`SELECT count(*) FROM public.whisper_nickname_versions n WHERE account_id=? AND nickname_version=? AND review_state='approved' AND revoked_at IS NULL AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=n.content_registry_id AND x.active)`, r.OwnerID, *r.NicknameVersion).Scan(&n).Error; e != nil {
			return false, e
		}
		if n != 1 {
			return false, nil
		}
	}
	if r.registry.Kind == "whisper_comment" {
		if r.ParentID == nil {
			return false, nil
		}
		parent, err := whisperTarget(db, "whisper_post", *r.ParentID)
		if errors.Is(err, command.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if _, err = whisperPublicBody(db, parent); errors.Is(err, command.ErrNotFound) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		e := db.Raw(`SELECT count(*) FROM public.whisper_posts p JOIN public.accounts a ON a.id=p.owner_id JOIN public.whisper_post_versions v ON v.post_id=p.id AND v.content_version=p.current_content_version WHERE p.id=? AND p.lifecycle='active' AND p.publish_intent AND p.comments_open AND p.comments_epoch=? AND p.published_content_version=? AND p.current_content_version=? AND v.review_state='approved' AND v.public_body IS NOT NULL AND a.lifecycle='active' AND a.verification='verified' AND NOT EXISTS(SELECT 1 FROM public.blocks WHERE (owner_id=? AND target_id=a.id) OR (owner_id=a.id AND target_id=?)) AND NOT EXISTS(SELECT 1 FROM public.penalties WHERE account_id=a.id AND revoked_at IS NULL AND action_kind IN ('mute','ban') AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp())) AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE x.active AND c.kind='whisper_post' AND c.entity_id=p.id) AND NOT EXISTS(SELECT 1 FROM public.privacy_holds WHERE target_kind='whisper_post' AND target_id=p.id AND status='active')`, *r.ParentID, r.CommentsEpoch, r.ParentVersion, r.ParentVersion, r.OwnerID, r.OwnerID).Scan(&n).Error
		return n == 1, e
	}
	return true, nil
}

func whisperDecrypt(ctx context.Context, db *gorm.DB, codec *securedata.Codec, r whisperSource) (body string, identity whisper.IdentityInput, err error) {
	var raw struct{ Body, Identity json.RawMessage }
	switch r.registry.Kind {
	case "whisper_post":
		err = db.Raw("SELECT body_cipher AS body,identity_cipher AS identity FROM public.whisper_post_versions WHERE content_registry_id=?", r.registry.ID).Scan(&raw).Error
	case "whisper_comment":
		err = db.Raw("SELECT body_cipher AS body,identity_cipher AS identity FROM public.whisper_comment_versions WHERE content_registry_id=?", r.registry.ID).Scan(&raw).Error
	case "whisper_nickname":
		err = db.Raw("SELECT label_cipher AS body FROM public.whisper_nickname_versions WHERE content_registry_id=?", r.registry.ID).Scan(&raw).Error
	case "whisper_capsule":
		err = db.Raw("SELECT body_cipher AS body FROM public.whisper_capsules WHERE id=?", r.ID).Scan(&raw).Error
	}
	if err != nil {
		return
	}
	object := r.ID
	purpose := r.registry.Kind + "_body"
	if r.registry.Kind == "whisper_nickname" {
		purpose = "whisper_nickname"
	}
	err = whisperPlain(ctx, codec, purpose, object, r.ContentVersion, raw.Body, &body)
	if err == nil && len(raw.Identity) > 0 {
		err = whisperPlain(ctx, codec, r.registry.Kind+"_identity", object, r.ContentVersion, raw.Identity, &identity)
	}
	return
}
func whisperPlain(ctx context.Context, codec *securedata.Codec, purpose, id string, v int64, raw json.RawMessage, out any) error {
	var env securedata.Envelope
	if json.Unmarshal(raw, &env) != nil {
		return command.ErrUnavailable
	}
	b, e := codec.Decrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(v)}, env)
	if e != nil {
		return e
	}
	defer clear(b)
	return json.Unmarshal(b, out)
}
