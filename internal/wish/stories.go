package wish

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// StoryInput 首次提交使用心愿聚合版本，提交成功即已还愿。
type StoryInput struct {
	Body                string `json:"body"`
	WishExpectedVersion int64  `json:"wish_expected_version"`
}

// StoryUpdate 的期望版本是所属心愿聚合版本，与故事内容版本分离。
type StoryUpdate struct {
	Body            string `json:"body"`
	ExpectedVersion int64  `json:"expected_version"`
}

// StoryAccepted 只确认当前稿件持久接收，公开结果通过本人心愿状态取得。
type StoryAccepted struct {
	ID             string `json:"id"`
	Version        int64  `json:"version"`
	ContentVersion int64  `json:"content_version"`
	Status         string `json:"status"`
	WishState      State  `json:"wish_state"`
}

func (s *Service) storyDone(op, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	s.policy.finish("完成还愿分享操作完成", op, rid, start, *err)
}

func validStoryBody(body string, max int) bool {
	return max > 0 && utf8.ValidString(body) && strings.TrimSpace(body) != "" && len(body) <= max
}

func (s *Service) storyDigest(body string) string {
	m := hmac.New(sha256.New, s.receiptKey)
	m.Write([]byte("story.body\x00"))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) storyOwner(ctx context.Context, db *gorm.DB, id, actor string, allowHold bool) (aggregate, error) {
	a, e := readAggregate(ctx, db, id, false)
	if e != nil {
		return a, e
	}
	if a.row.OwnerID != actor {
		return a, command.ErrNotFound
	}
	if !a.owner.Active || !a.owner.Verified || a.owner.Restricted || a.snapshot.Restricted || !allowHold && a.snapshot.PrivacyHeld {
		return a, command.ErrForbidden
	}
	var n int64
	if e = db.WithContext(ctx).Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind='story' AND c.entity_id=? AND r.active", id).Count(&n).Error; e != nil {
		return a, e
	}
	if n > 0 {
		return a, command.ErrForbidden
	}
	return a, nil
}

func currentParticipant(ctx context.Context, db *gorm.DB, id string, now time.Time) (m entity.Match, c entity.Conversation, err error) {
	db = db.WithContext(ctx)
	if err = db.Where("wish_id=? AND status='current'", id).Take(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = command.ErrConflict
		}
		return
	}
	ok, e := participantAllowed(ctx, db, m.HelperID, now)
	if e != nil {
		err = e
		return
	}
	if !ok {
		err = command.ErrForbidden
		return
	}
	denied, e := blocked(ctx, db, m.RequesterID, m.HelperID)
	if e != nil {
		err = e
		return
	}
	if denied {
		err = command.ErrBlockedInteraction
		return
	}
	err = db.Where("match_id=?", m.ID).Take(&c).Error
	return
}

// Complete 原子确认实现并关闭会话写入，所有待审消息永久取消，普通回退仍保留原关系。
func (s *Service) Complete(ctx context.Context, token, key, rid, id string, version int64) (out CreatedDraft, err error) {
	defer func() {
		if err != nil {
			out = CreatedDraft{}
		}
	}()
	defer s.storyDone("wish.complete", rid, time.Now(), &err)
	if !validWishID(id) || version < 1 || !validCommandKey(key) {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "wish.complete", Method: "POST", ResourcePath: "/api/v1/wishes/" + id + "/complete", ActorKind: "student", Access: command.Verified, RequiredExpectations: []string{"expected_version"}, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverInteraction(ctx, db, id)
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		_, e := s.storyOwner(ctx, tx.DB(), id, tx.Actor().ID, false)
		if e != nil {
			return e
		}
		_, _, e = currentParticipant(ctx, tx.DB(), id, tx.Now())
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		a, e := readAggregate(ctx, tx.DB(), id, false)
		if e != nil {
			return "", e
		}
		if a.row.State != "matching" || a.row.Version == math.MaxInt64 {
			return "", command.ErrConflict
		}
		_, c, e := currentParticipant(ctx, tx.DB(), id, tx.Now())
		if e != nil {
			return "", e
		}
		if e = cancelConversation(ctx, tx, c); e != nil {
			return "", e
		}
		next := a.snapshot
		next.State = Realized
		next.Version++
		return id, saveState(ctx, tx, a, next)
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		if ref != id {
			return command.ErrNotFound
		}
		var e error
		out, e = resolveCreated(ctx, tx.DB(), id, tx.Actor().ID)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: version, Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.Wish, ID: id}, Value: version}}})
	return
}

// SubmitStory 将非空原稿与审核工作原子保存，同时把已实现心愿转为已还愿。
func (s *Service) SubmitStory(ctx context.Context, token, key, rid, id string, in StoryInput) (StoryAccepted, error) {
	return s.writeStory(ctx, token, key, rid, id, in.Body, in.WishExpectedVersion, false)
}

// UpdateStory 保存新的待审内容版本，旧版立即停止公开；不回退心愿已还愿状态。
func (s *Service) UpdateStory(ctx context.Context, token, key, rid, id string, in StoryUpdate) (StoryAccepted, error) {
	return s.writeStory(ctx, token, key, rid, id, in.Body, in.ExpectedVersion, true)
}

func (s *Service) writeStory(ctx context.Context, token, key, rid, id, body string, version int64, update bool) (out StoryAccepted, err error) {
	defer func() {
		if err != nil {
			out = StoryAccepted{}
		}
	}()
	defer s.storyDone("story.submit", rid, time.Now(), &err)
	if !validWishID(id) || !validCommandKey(key) || version < 1 || !validStoryBody(body, s.config.MaxStoryBytes) {
		return out, command.ErrInvalid
	}
	op, method, suffix, name := "story.create", "POST", "/stories", "wish_expected_version"
	if update {
		op, method, suffix, name = "story.update", "PUT", "/story", "expected_version"
	}
	d := command.Definition{Kind: op, Method: method, ResourcePath: "/api/v1/wishes/" + id + suffix, ActorKind: "student", Access: command.Verified, RequiredExpectations: []string{name}, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverStory(ctx, db, id)
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		_, e := s.storyOwner(ctx, tx.DB(), id, tx.Actor().ID, update)
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		a, e := readAggregate(ctx, db, id, false)
		if e != nil {
			return "", e
		}
		if a.row.Version == math.MaxInt64 || a.row.CurrentStoryVersion == math.MaxInt64 {
			return "", command.ErrConflict
		}
		var source *string
		var peerEpoch *int64
		if update {
			if a.row.State != "fulfilled" || a.row.CurrentStoryVersion == 0 {
				return "", command.ErrConflict
			}
			old, e := storyRow(ctx, db, id, a.row.CurrentStoryVersion)
			if e != nil {
				return "", e
			}
			source, peerEpoch = old.SourceMatchID, old.PeerEpoch
		} else {
			if a.row.State != "realized" || a.row.CurrentStoryVersion != 0 {
				return "", command.ErrConflict
			}
			m, _, e := currentParticipant(ctx, db, id, tx.Now())
			if e != nil {
				return "", e
			}
			var peer entity.Account
			if e = db.Where("id=?", m.HelperID).Take(&peer).Error; e != nil {
				return "", e
			}
			source, peerEpoch = &m.ID, &peer.AuthEpoch
		}
		holds, e := ReadStoryHolds(ctx, db, id)
		if e != nil {
			return "", e
		}
		nextVersion := a.row.CurrentStoryVersion + 1

		if source != nil {
			var peers []struct{ AuthEpoch int64 }
			if e = db.Table("public.matches AS m").Select("p.auth_epoch").Joins("JOIN public.accounts p ON p.id=m.helper_id AND p.lifecycle='active'").Where("m.id=?", *source).Scan(&peers).Error; e != nil {
				return "", e
			}
			if len(peers) != 1 {
				if !holds.Active || nextVersion < holds.RequiredVersion {
					return "", command.ErrForbidden
				}
				source, peerEpoch = nil, nil
			} else {
				peerEpoch = &peers[0].AuthEpoch
			}
		}

		if e = invalidateStories(ctx, tx, id); e != nil {
			return "", e
		}
		plain, _ := json.Marshal(body)
		defer clear(plain)
		envelope, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "story_content", ObjectID: id, Version: uint64(nextVersion)}, plain)
		if e != nil {
			return "", e
		}
		raw, e := json.Marshal(envelope)
		if e != nil {
			return "", e
		}
		registryID, taskID := uuid.NewString(), uuid.NewString()
		if e = db.Create(&entity.ContentRegistry{ID: registryID, Kind: "story", EntityID: id, AuthorID: a.row.OwnerID, ContentVersion: nextVersion, SchoolDetectionVersion: "pending"}).Error; e != nil {
			return "", e
		}
		epoch := a.row.RestrictionEpoch
		if e = db.Create(&entity.ModerationTask{PrivacyRecheck: holds.Active || holds.ParentActive, ID: taskID, ContentID: registryID, ContentVersion: nextVersion, SubjectEpoch: tx.Actor().AuthEpoch, RestrictionEpoch: &epoch, HoldEpoch: holds.Epoch, CreatedAt: tx.Now()}).Error; e != nil {
			return "", e
		}
		if e = db.Create(&entity.StoryVersion{WishID: id, StoryVersion: nextVersion, AuthorID: a.row.OwnerID, BodyCipher: raw, ReviewTaskID: &taskID, SchoolDetectionVersion: "pending", SourceMatchID: source, PeerEpoch: peerEpoch, ParentHoldEpoch: holds.ParentEpoch}).Error; e != nil {
			return "", e
		}
		if e = db.Model(&entity.Wish{}).Where("id=?", id).Updates(map[string]any{"state": "fulfilled", "current_story_version": nextVersion, "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		if e = durable.Append(ctx, db, s.policy.logger, ReviewRoute(), durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: taskID, AggregateVersion: nextVersion}); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		if ref != id {
			return command.ErrNotFound
		}
		var w entity.Wish
		if e := tx.DB().WithContext(ctx).Where("id=? AND owner_id=? AND state='fulfilled'", id, tx.Actor().ID).Take(&w).Error; e != nil {
			return e
		}
		out = StoryAccepted{ID: id, Version: w.Version, ContentVersion: w.CurrentStoryVersion, Status: "pending_review", WishState: Fulfilled}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: struct {
		Body    string
		Version int64
	}{s.storyDigest(body), version}, Expectations: []command.Expectation{{Name: name, Target: command.Resource{Kind: command.Wish, ID: id}, Value: version}}})
	return
}

// WithdrawStory 撤回当前故事并使旧工作失效，不退回已实现或恢复聊天写入。
func (s *Service) WithdrawStory(ctx context.Context, token, key, rid, id string, version int64) (out CreatedDraft, err error) {
	defer func() {
		if err != nil {
			out = CreatedDraft{}
		}
	}()
	defer s.storyDone("story.withdraw", rid, time.Now(), &err)
	if !validWishID(id) || !validCommandKey(key) || version < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "story.withdraw", Method: "POST", ResourcePath: "/api/v1/wishes/" + id + "/story/withdraw", ActorKind: "student", Access: command.Verified, RequiredExpectations: []string{"expected_version"}, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverStory(ctx, db, id)
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		_, e := s.storyOwner(ctx, tx.DB(), id, tx.Actor().ID, true)
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		var w entity.Wish
		if e := db.Where("id=?", id).Take(&w).Error; e != nil {
			return "", e
		}
		if w.State != "fulfilled" || w.CurrentStoryVersion == 0 {
			return "", command.ErrConflict
		}
		v, e := storyRow(ctx, db, id, w.CurrentStoryVersion)
		if e != nil {
			return "", e
		}
		if v.WithdrawnAt != nil {
			return "", command.ErrConflict
		}
		if e = invalidateStories(ctx, tx, id); e != nil {
			return "", e
		}
		if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", id, w.CurrentStoryVersion).Update("withdrawn_at", tx.Now()).Error; e != nil {
			return "", e
		}
		return id, db.Model(&entity.Wish{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		if ref != id {
			return command.ErrNotFound
		}
		var e error
		out, e = resolveCreated(ctx, tx.DB(), id, tx.Actor().ID)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: version, Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.Wish, ID: id}, Value: version}}})
	return
}
