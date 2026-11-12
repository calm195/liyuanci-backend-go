package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
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

func whisperPlan(ctx context.Context, db *gorm.DB, id string) (command.Discovery, error) {
	r, e := loadWhisper(ctx, db, id)
	if errors.Is(e, command.ErrNotFound) {
		return command.Discovery{}, nil
	}
	if e != nil {
		return command.Discovery{}, e
	}
	kind := map[string]command.ResourceKind{
		"whisper_post":     command.WhisperPost,
		"whisper_comment":  command.WhisperComment,
		"whisper_nickname": command.WhisperNickname,
		"whisper_capsule":  command.WhisperCapsule,
	}[r.registry.Kind]
	p := command.Discovery{
		Accounts:  []string{r.OwnerID},
		Resources: []command.Resource{{Kind: kind, ID: r.ID}, {Kind: command.ModerationTask, ID: id}},
	}
	if r.ParentID != nil {
		var parent entity.WhisperPost
		if e = db.WithContext(ctx).Select("id,owner_id").Where("id=?", *r.ParentID).Take(&parent).Error; e != nil {
			return p, e
		}
		p.Accounts = append(p.Accounts, parent.OwnerID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperPost, ID: parent.ID})
	}
	if r.registry.Kind == "whisper_comment" {
		var c entity.WhisperComment
		if e = db.WithContext(ctx).Select("id,post_id,author_id,content_version,parent_content_version,root_comment_id,reply_to_comment_id").Where("id=?", r.ID).Take(&c).Error; e != nil {
			return p, e
		}
		for _, id := range []*string{c.RootCommentID, c.ReplyToCommentID} {
			if id == nil {
				continue
			}
			var target entity.WhisperComment
			if e = db.WithContext(ctx).Select("id,author_id").Where("id=?", *id).Take(&target).Error; e != nil {
				return p, e
			}
			p.Accounts = append(p.Accounts, target.AuthorID)
			p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperComment, ID: target.ID})
		}
	}
	// 昵称的撤销与公开决定共享同一资源锁，避免批准后立即泄漏旧快照。
	if r.NicknameVersion != nil {
		var n entity.WhisperNicknameVersion
		if e = db.WithContext(ctx).Select("content_registry_id").Where(
			"account_id=? AND nickname_version=?",
			r.OwnerID,
			*r.NicknameVersion,
		).Take(&n).Error; e != nil {
			return p, e
		}
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperNickname, ID: n.ContentRegistryID})
	}
	return p, nil
}

func whisperCurrent(r whisperSource) bool {
	return r.TaskID != nil && *r.TaskID == r.task.ID && r.ContentVersion == r.task.ContentVersion && r.Lifecycle == "active" &&
		r.account.Lifecycle == "active" &&
		r.account.AuthEpoch == r.task.SubjectEpoch
}

func whisperEligible(ctx context.Context, db *gorm.DB, r whisperSource) (bool, error) {
	if !whisperCurrent(r) || r.task.HoldEpoch != r.PrivacyEpoch || r.task.RestrictionEpoch == nil ||
		*r.task.RestrictionEpoch != r.RestrictionEpoch {
		return false, nil
	}
	if r.registry.Kind == "whisper_post" && r.account.Verification != "verified" {
		return false, nil
	}
	if r.registry.Kind == "whisper_post" && !r.PublishIntent {
		return false, nil
	}
	n, e := countWhisperBarriers(ctx, db, r)
	if e != nil {
		return false, e
	}
	if n != 0 {
		return false, nil
	}
	if r.registry.Kind == "whisper_nickname" {
		n, e := countWhisperNicknameReferences(ctx, db, r)
		if e != nil {
			return false, e
		}
		return n == 1, nil
	}
	if r.NicknameVersion != nil {
		n, e := countApprovedWhisperNickname(ctx, db, r)
		if e != nil {
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
		parent, err := whisperTarget(ctx, db, "whisper_post", *r.ParentID)
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
		n, e := countCommentableWhisperParent(ctx, db, r)
		if e != nil || n != 1 {
			return false, e
		}
		var c entity.WhisperComment
		if e = db.WithContext(ctx).Select("id,post_id,author_id,content_version,parent_content_version,root_comment_id,reply_to_comment_id").Where("id=?", r.ID).Take(&c).Error; e != nil {
			return false, e
		}
		var now time.Time
		if e = db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return false, e
		}
		return whisper.ReplyContextEligible(ctx, db, c, now)
	}
	return true, nil
}

func whisperDecrypt(ctx context.Context, db *gorm.DB, codec *securedata.Codec, r whisperSource) (body string, identity whisper.IdentityInput, err error) {
	raw, err := readWhisperCipher(ctx, db, r)
	if err != nil {
		return "", identity, err
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

// whisperTitle 读取与精确正文版本绑定的标题，历史版本使用安全默认名称。
func whisperTitle(ctx context.Context, db *gorm.DB, codec *securedata.Codec, r whisperSource) (string, error) {
	var v entity.WhisperPostVersion
	if e := db.WithContext(ctx).Select("title_cipher").Where("post_id=? AND content_version=?", r.ID, r.ContentVersion).Take(&v).Error; e != nil {
		return "", e
	}
	title := "未命名悄悄话"
	if len(v.TitleCipher) > 0 {
		if e := whisperPlain(ctx, codec, "whisper_post_title", r.ID, r.ContentVersion, v.TitleCipher, &title); e != nil {
			return "", e
		}
	}
	return title, nil
}
