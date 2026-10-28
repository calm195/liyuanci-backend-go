package whisper

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// SetHug 串行化同一父帖的目标态，取消保留最小记录，真实改变才递增独立版本。
func (s *Service) SetHug(ctx context.Context, token, key, rid, id string, in HugInput) (out HugResult, err error) {
	defer s.completed("hug_set", rid, time.Now(), &err)
	if !ValidID(id) || in.ExpectedPostVersion < 1 || in.ExpectedHugVersion < 0 {
		return out, command.ErrInvalid
	}
	access := command.Ordinary
	if !in.Active {
		access = command.Restricted
	}
	d := command.Definition{Kind: "whisper.hug.set", Method: "PUT", ResourcePath: "/api/v1/whispers/posts/" + id + "/hugs", ActorKind: "student", Access: access, RetainReceipt: true, RequiredExpectations: []string{"expected_post_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return interactionPlan(ctx, db, id, "")
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		var p entity.WhisperPost
		if e := tx.DB().WithContext(ctx).Select("owner_id").Where("id=?", id).Take(&p).Error; e != nil {
			return missingRow(e)
		}
		if p.OwnerID == tx.Actor().ID {
			return command.ErrBlockedInteraction
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		c, e := s.resources(ctx)
		if e != nil {
			return "", e
		}
		if _, e = s.publicPostBase(ctx, tx.DB(), tx.Now(), id, audience{ID: tx.Actor().ID}, c); e != nil {
			return "", e
		}
		var p entity.WhisperPost
		if e = tx.DB().WithContext(ctx).Select("owner_id").Where("id=?", id).Take(&p).Error; e != nil {
			return "", e
		}
		if in.Active {
			if e = interactionBlocked(ctx, tx.DB(), tx.Actor().ID, p.OwnerID); e != nil {
				return "", e
			}
		}
		h := entity.WhisperHug{}
		e = tx.DB().WithContext(ctx).Where("post_id=? AND actor_id=?", id, tx.Actor().ID).Take(&h).Error
		missing := errors.Is(e, gorm.ErrRecordNotFound)
		if e != nil && !missing {
			return "", e
		}
		if h.Version != in.ExpectedHugVersion {
			return "", command.ErrVersionConflict
		}
		if missing {
			// 显式取消从未抱抱的对象也建立 false tombstone，阻断旧的 version=0 写入。
			return id, tx.DB().WithContext(ctx).Exec("INSERT INTO public.whisper_hugs(post_id,actor_id,active,version) VALUES (?,?,?,1)", id, tx.Actor().ID, in.Active).Error
		}
		if h.Active == in.Active {
			return id, nil
		}
		return id, tx.DB().WithContext(ctx).Model(&entity.WhisperHug{}).Where("post_id=? AND actor_id=?", id, tx.Actor().ID).Updates(map[string]any{"active": in.Active, "version": h.Version + 1}).Error
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		c, e := s.resources(ctx)
		if e != nil {
			return e
		}
		if _, e = s.publicPostBase(ctx, tx.DB(), tx.Now(), ref, audience{ID: tx.Actor().ID}, c); e != nil {
			return e
		}
		out = HugResult{PostID: ref}
		if e = countHugs(ctx, tx.DB(), ref, &out.HugCount); e != nil {
			return e
		}
		var h entity.WhisperHug
		if e = tx.DB().WithContext(ctx).Where("post_id=? AND actor_id=?", ref, tx.Actor().ID).Take(&h).Error; e != nil {
			return e
		}
		out.MyHug = HugState{h.Active, h.Version}
		return nil
	}
	_, err = s.execute(ctx, d, token, key, rid, in, 0, "", "hug", command.Expectation{Name: "expected_post_version", Target: command.Resource{Kind: command.WhisperPost, ID: id}, Value: in.ExpectedPostVersion})
	return
}
