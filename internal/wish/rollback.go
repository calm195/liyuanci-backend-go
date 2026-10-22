package wish

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// RollbackInput 的版本绑定心愿聚合；matching只恢复原匹配，published是解绑重新招募。
type RollbackInput struct {
	ExpectedVersion int64 `json:"expected_version"`
	Target          State `json:"target"`
	RefreshExpiry   bool  `json:"refresh_expiry"`
}

// Rollback 普通回退只允许许愿人，处罚/拉黑例外退出属于独立业务入口。
// 所有关系、会话代次、旧异步任务和新审核与心愿状态同一事务提交。
func (s *Service) Rollback(ctx context.Context, token, key, rid, id string, in RollbackInput) (out CreatedDraft, err error) {
	defer func() {
		if err != nil {
			out = CreatedDraft{}
		}
	}()
	defer s.interactionDone("wish.rollback", rid, time.Now(), &err)
	if !validWishID(id) || !validCommandKey(key) || in.ExpectedVersion < 1 || (in.Target != Matching && in.Target != Published) || (in.Target == Matching && in.RefreshExpiry) {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "wish.rollback", Method: "POST", ResourcePath: "/api/v1/wishes/" + id + "/rollback", ActorKind: "student", Access: command.Verified, RequiredExpectations: []string{"expected_version"}, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverInteraction(ctx, db, id)
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		a, e := readAggregate(ctx, tx.DB(), id, false)
		if e != nil {
			return e
		}
		if a.row.OwnerID != tx.Actor().ID {
			return command.ErrNotFound
		}
		if !ordinary(a.owner, a.snapshot) {
			return command.ErrForbidden
		}
		// 回执重放也复核当前参与人，已解绑时不能拿旧回执恢复历史关系。
		var matches []entity.Match
		if e = tx.DB().WithContext(ctx).Where("wish_id=? AND status='current'", id).Find(&matches).Error; e != nil {
			return e
		}
		for _, m := range matches {
			ok, e := participantAllowed(ctx, tx.DB(), m.HelperID, tx.Now())
			if e != nil {
				return e
			}
			if !ok {
				return command.ErrForbidden
			}
			denied, e := blocked(ctx, tx.DB(), m.RequesterID, m.HelperID)
			if e != nil {
				return e
			}
			if denied {
				return command.ErrBlockedInteraction
			}
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		a, e := readAggregate(ctx, tx.DB(), id, false)
		if e != nil {
			return "", e
		}
		if a.row.Version == math.MaxInt64 {
			return "", command.ErrConflict
		}
		var m entity.Match
		if e = tx.DB().WithContext(ctx).Where("wish_id=? AND status='current'", id).Take(&m).Error; e != nil {
			return "", command.ErrConflict
		}
		var conversation entity.Conversation
		if e = tx.DB().WithContext(ctx).Where("match_id=?", m.ID).Take(&conversation).Error; e != nil {
			return "", e
		}
		if in.Target == Matching {
			if a.snapshot.State != Realized {
				return "", command.ErrConflict
			}
			var count int64
			if e = tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where("owner_id=? AND draft_deleted_at IS NULL AND (state='matching' OR (state='published' AND expires_at>?))", a.row.OwnerID, tx.Now()).Count(&count).Error; e != nil {
				return "", e
			}
			if count >= 3 {
				return "", command.ErrActiveWishLimit
			}
		} else if a.snapshot.State != Matching {
			return "", command.ErrConflict
		}
		// 每次权限变更递增会话代次；已实现期间及解绑前的待审消息永久取消，恢复不重放。
		if e = cancelConversation(ctx, tx, conversation); e != nil {
			return "", e
		}
		next := a.snapshot
		next.Version++
		if in.Target == Matching {
			next.State = Matching
		} else {
			if e = tx.DB().WithContext(ctx).Model(&entity.Match{}).Where("id=?", m.ID).Updates(map[string]any{"status": "ended", "ended_at": tx.Now(), "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
			if m.ApplicationID != nil {
				row, e := readApplication(ctx, tx.DB(), *m.ApplicationID)
				if e != nil {
					return "", e
				}
				if e = CancelApplication(ctx, tx.DB(), row, "invalidated", "match_ended"); e != nil {
					return "", e
				}
			}
			if e = invalidateApplications(ctx, tx, id, "wish_recruiting_again"); e != nil {
				return "", e
			}
			if in.RefreshExpiry {
				if e = s.refreshReview(ctx, tx, a, &next); e != nil {
					return "", e
				}
			} else {
				if !reviewReady(a.snapshot) {
					return "", command.ErrConflict
				}
				next.State = Published
				if next.ExpiresAt == nil || !tx.Now().Before(*next.ExpiresAt) {
					next.State = Expired
				} else {
					at := tx.Now()
					next.PublishedAt = &at
				}
			}
		}
		if e = saveState(ctx, tx, a, next); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		if ref != id {
			return command.ErrNotFound
		}
		var e error
		out, e = resolveCreated(ctx, tx.DB(), id, tx.Actor().ID)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: in, Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.Wish, ID: id}, Value: in.ExpectedVersion}}})
	return
}

func cancelConversation(ctx context.Context, tx *command.Tx, c entity.Conversation) error {
	db := tx.DB().WithContext(ctx)
	if e := db.Model(&entity.Conversation{}).Where("id=?", c.ID).Update("write_epoch", gorm.Expr("write_epoch+1")).Error; e != nil {
		return e
	}
	var ids []string
	if e := db.Model(&entity.Message{}).Where("conversation_id=? AND status IN ?", c.ID, []string{"pending_scan", "pending_review"}).Pluck("id", &ids).Error; e != nil {
		return e
	}
	if e := db.Model(&entity.Message{}).Where("id IN ?", ids).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	return obsoleteWork(ctx, tx, "message", ids)
}

func (s *Service) refreshReview(ctx context.Context, tx *command.Tx, a aggregate, next *Snapshot) error {
	if a.row.CurrentContentVersion == math.MaxInt64 {
		return command.ErrConflict
	}
	var envelope securedata.Envelope
	if json.Unmarshal(a.content.BodyCipher, &envelope) != nil {
		return command.ErrUnavailable
	}
	plain, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: a.row.ID, Version: uint64(a.row.CurrentContentVersion)}, envelope)
	if e != nil {
		return command.ErrUnavailable
	}
	defer clear(plain)
	next.ContentVersion++
	next.State = PendingReview
	next.RefreshOnPublish = true
	next.Review = Review{}
	wrapped, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: a.row.ID, Version: uint64(next.ContentVersion)}, plain)
	if e != nil {
		return command.ErrUnavailable
	}
	cipher, e := json.Marshal(wrapped)
	if e != nil {
		return e
	}
	if e = invalidate(ctx, tx, a.row.ID, false, "wish_refreshed"); e != nil {
		return e
	}
	registryID, taskID := uuid.NewString(), uuid.NewString()
	if e = tx.DB().WithContext(ctx).Create(&entity.WishVersion{WishID: a.row.ID, ContentVersion: next.ContentVersion, BodyCipher: cipher, RewardMethod: a.content.RewardMethod, IdentityTags: json.RawMessage(`{}`), SchoolDetectionVersion: "pending"}).Error; e != nil {
		return e
	}
	if e = tx.DB().WithContext(ctx).Create(&entity.ContentRegistry{ID: registryID, Kind: "wish", EntityID: a.row.ID, AuthorID: a.row.OwnerID, ContentVersion: next.ContentVersion, SchoolDetectionVersion: "pending"}).Error; e != nil {
		return e
	}
	epoch := a.row.RestrictionEpoch
	if e = tx.DB().WithContext(ctx).Create(&entity.ModerationTask{ID: taskID, ContentID: registryID, ContentVersion: next.ContentVersion, SubjectEpoch: a.account.AuthEpoch, RestrictionEpoch: &epoch, HoldEpoch: a.snapshot.HoldEpoch, CreatedAt: tx.Now()}).Error; e != nil {
		return e
	}
	if e = tx.DB().WithContext(ctx).Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", a.row.ID, next.ContentVersion).Update("review_task_id", taskID).Error; e != nil {
		return e
	}
	return durable.Append(ctx, tx.DB(), s.policy.logger, ReviewRoute(), durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: taskID, AggregateVersion: next.ContentVersion})
}
