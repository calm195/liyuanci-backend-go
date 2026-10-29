package privacy

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Redactor 由宿主显式注入个人及组合信息脱敏能力，不传注销人的身份。
// 只返回保留作者自有语义的脱敏原稿，仍须普通扫描及人工审核；缺失/失败不降级为保留旧原稿。
type Redactor interface {
	Redact(context.Context, string, string) (string, error)
}

type redaction struct {
	w       entity.Wish
	content wish.Content
	story   *string
}

func (s *Service) decrypt(ctx context.Context, purpose, id string, version int64, raw json.RawMessage, out any) error {
	var env securedata.Envelope
	if json.Unmarshal(raw, &env) != nil {
		return command.ErrUnavailable
	}
	b, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, env)
	defer clear(b)
	if e != nil || json.Unmarshal(b, out) != nil {
		return command.ErrUnavailable
	}
	return nil
}
func (s *Service) encrypt(ctx context.Context, purpose, id string, version int64, v any) (json.RawMessage, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	defer clear(b)
	env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, b)
	if e != nil {
		return nil, e
	}
	return json.Marshal(env)
}
func (s *Service) redact(ctx context.Context, kind, body string) (out string, err error) {
	defer func() {
		if recover() != nil {
			out = ""
			err = command.ErrUnavailable
		}
	}()
	if s.cfg.Redactor == nil {
		return "", command.ErrUnavailable
	}
	out, err = s.cfg.Redactor.Redact(ctx, kind, body)
	max := 20000
	if kind == "wish" || kind == "whisper_comment" {
		max = 500
	}
	if err != nil || !utf8.ValidString(out) || strings.TrimSpace(out) == "" || utf8.RuneCountInString(out) > max {
		return "", command.ErrUnavailable
	}
	return
}
func (s *Service) prepareRedactions(ctx context.Context, in Intent) (out []redaction, err error) {
	if in.Scope != "account" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	var rows []entity.Wish
	if err = s.db.WithContext(ctx).Where("owner_id<>? AND id IN (SELECT wish_id FROM public.matches WHERE helper_id=?)", in.Target, in.Target).Find(&rows).Error; err != nil {
		return
	}
	for _, w := range rows {
		var v entity.WishVersion
		if err = s.db.WithContext(ctx).Where("wish_id=? AND content_version=?", w.ID, w.CurrentContentVersion).Take(&v).Error; err != nil {
			return
		}
		r := redaction{w: w}
		if err = s.decrypt(ctx, "wish_content", w.ID, w.CurrentContentVersion, v.BodyCipher, &r.content); err != nil {
			return
		}
		r.content.Body, err = s.redact(ctx, "wish", r.content.Body)
		if err != nil {
			return
		}
		r.content.IdentityTags = wish.IdentityTags{}
		if w.CurrentStoryVersion > 0 {
			var v entity.StoryVersion
			var body string
			if err = s.db.WithContext(ctx).Where("wish_id=? AND story_version=?", w.ID, w.CurrentStoryVersion).Take(&v).Error; err != nil {
				return
			}
			if err = s.decrypt(ctx, "story_content", w.ID, w.CurrentStoryVersion, v.BodyCipher, &body); err != nil {
				return
			}
			body, err = s.redact(ctx, "story", body)
			if err != nil {
				return
			}
			r.story = &body
		}
		out = append(out, r)
	}
	return
}

// applyRedactions 把所有旧原稿及扫描候选中的对方身份引用清除，并生成等待作者显式重提的新版本，不自动送审。
// 旧版本仅留不可公开的已脱敏作者内容，不留原应愿人标识；屏障仍等待新版本逐一审批。
func (s *Service) applyRedactions(ctx context.Context, db *gorm.DB, rows []redaction) error {
	for _, r := range rows {
		var w entity.Wish
		if e := db.Where("id=?", r.w.ID).Take(&w).Error; e != nil {
			return e
		}
		if w.Version != r.w.Version {
			return command.ErrRetry
		}
		var a entity.Account
		if e := db.Where("id=? AND lifecycle='active'", w.OwnerID).Take(&a).Error; e != nil {
			return e
		}
		var old []entity.WishVersion
		if e := db.Where("wish_id=?", w.ID).Find(&old).Error; e != nil {
			return e
		}
		for _, v := range old {
			raw, e := s.encrypt(ctx, "wish_content", w.ID, v.ContentVersion, r.content)
			if e != nil {
				return e
			}
			if e = db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", w.ID, v.ContentVersion).Update("body_cipher", raw).Error; e != nil {
				return e
			}
		}
		var stories []entity.StoryVersion
		if e := db.Where("wish_id=?", w.ID).Find(&stories).Error; e != nil {
			return e
		}
		for _, v := range stories {
			if r.story == nil {
				return command.ErrUnavailable
			}
			raw, e := s.encrypt(ctx, "story_content", w.ID, v.StoryVersion, *r.story)
			if e != nil {
				return e
			}
			if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", w.ID, v.StoryVersion).Updates(map[string]any{"body_cipher": raw, "source_match_id": nil, "peer_epoch": nil}).Error; e != nil {
				return e
			}
		}
		taskSub := "SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry c ON c.id=t.content_id WHERE c.entity_id=? AND c.kind IN ('wish','story')"
		if e := db.Exec("DELETE FROM public.moderation_scans WHERE task_id IN ("+taskSub+")", w.ID).Error; e != nil {
			return e
		}
		if e := db.Exec("UPDATE public.moderation_tasks SET reason=NULL,revision_suggestion=NULL,decision=NULL WHERE id IN ("+taskSub+")", w.ID).Error; e != nil {
			return e
		}
		next := w.CurrentContentVersion + 1
		raw, e := s.encrypt(ctx, "wish_content", w.ID, next, r.content)
		if e != nil {
			return e
		}
		holds, e := wish.ReadStoryHolds(ctx, db, w.ID)
		if e != nil {
			return e
		}
		if e = db.Create(&entity.ContentRegistry{ID: uuid.NewString(), Kind: "wish", EntityID: w.ID, ContentVersion: next, AuthorID: w.OwnerID, SchoolDetectionVersion: "pending"}).Error; e != nil {
			return e
		}
		if e = db.Create(&entity.WishVersion{WishID: w.ID, ContentVersion: next, BodyCipher: raw, IdentityTags: emptyJSON(), RewardMethod: r.content.RewardMethod, SchoolDetectionVersion: "pending"}).Error; e != nil {
			return e
		}
		state := "withdrawn"
		if w.State == "fulfilled" || w.State == "matching" || w.State == "realized" {
			state = w.State
		}
		updates := map[string]any{"current_content_version": next, "version": gorm.Expr("version+1"), "state": state}
		if r.story != nil {
			snext := w.CurrentStoryVersion + 1
			body, e := s.encrypt(ctx, "story_content", w.ID, snext, *r.story)
			if e != nil {
				return e
			}
			if e = db.Create(&entity.ContentRegistry{ID: uuid.NewString(), Kind: "story", EntityID: w.ID, ContentVersion: snext, AuthorID: w.OwnerID, SchoolDetectionVersion: "pending"}).Error; e != nil {
				return e
			}
			if e = db.Create(&entity.StoryVersion{WishID: w.ID, StoryVersion: snext, AuthorID: w.OwnerID, BodyCipher: body, IdentityTags: emptyJSON(), SchoolDetectionVersion: "pending", ParentHoldEpoch: holds.ParentEpoch}).Error; e != nil {
				return e
			}
			updates["current_story_version"] = snext
		}
		if e = db.Model(&entity.Wish{}).Where("id=?", w.ID).Updates(updates).Error; e != nil {
			return e
		}
	}
	return nil
}
