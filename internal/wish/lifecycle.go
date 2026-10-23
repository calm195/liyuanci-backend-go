package wish

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// UpdateInput 始终是完整内容替换；刷新期限也必须形成新版本并重新送审。
type UpdateInput struct {
	ContentInput
	ExpectedVersion int64 `json:"expected_version"`
	RefreshExpiry   bool  `json:"refresh_expiry"`
}

// ReviewRoute 返回固定的审核工作路由，消费者必须复核任务、版本和当前授权，不能直接信任唤醒。
func ReviewRoute() durable.Route {
	return durable.Route{
		Kind:       "wish_review",
		TargetKind: "moderation_task",
		EventType:  "wish.review_requested",
	}
}

func validWishID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
func validCommandKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}

// Update 原子保存加密新版本并作废旧审核与已上架心愿的未终结申请，不自动送审。
func (s *Service) Update(ctx context.Context, token, key, rid, id string, input UpdateInput) (CreatedDraft, error) {
	return s.mutate(ctx, token, key, rid, id, Change{Action: Edit, ExpectedVersion: input.ExpectedVersion, RefreshExpiry: input.RefreshExpiry}, &input.ContentInput, "")
}

// Submit 持久接收指定内容版本的送审，返回待审而不是公开成功。
func (s *Service) Submit(ctx context.Context, token, key, rid, id string, version, contentVersion int64) (CreatedDraft, error) {
	return s.mutate(ctx, token, key, rid, id, Change{Action: SubmitReview, ExpectedVersion: version, ExpectedContentVersion: contentVersion}, nil, "")
}

// Publish 在同作者guard下重算全部活跃额度并复核精确审核，满额保持ready。
func (s *Service) Publish(ctx context.Context, token, key, rid, id string, version int64) (CreatedDraft, error) {
	return s.mutate(ctx, token, key, rid, id, Change{Action: Publish, ExpectedVersion: version}, nil, "")
}

// Withdraw 撤下当前未过期的上架心愿，并原子作废未终结申请。
func (s *Service) Withdraw(ctx context.Context, token, key, rid, id string, version int64) (CreatedDraft, error) {
	return s.mutate(ctx, token, key, rid, id, Change{Action: Withdraw, ExpectedVersion: version}, nil, "")
}

// Delete 只对当前草稿生效；删除标记、密文清除和旧工作取消同事务，技术引用保留供重放复核。
func (s *Service) Delete(ctx context.Context, token, key, rid, id string, version int64) error {
	_, err := s.mutate(ctx, token, key, rid, id, Change{Action: DeleteDraft, ExpectedVersion: version}, nil, "")
	return err
}

func (s *Service) mutate(ctx context.Context, token, key, rid, id string, change Change, content *ContentInput, digest string) (out CreatedDraft, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		if err != nil {
			out = CreatedDraft{}
		}
		s.policy.finish("心愿变更操作完成", string(change.Action), rid, start, err)
	}()
	if content != nil {
		digest, err = s.requestDigest(*content)
		if err != nil {
			return out, err
		}
	}
	if !validWishID(id) || !validCommandKey(key) || change.ExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	path := "/api/v1/wishes/" + id + "/" + string(change.Action)
	method := "POST"
	switch change.Action {
	case Edit:
		path = "/api/v1/wishes/" + id + "/content"
		method = "PUT"
	case SubmitReview:
		path = "/api/v1/wishes/" + id + "/submit-review"
	case DeleteDraft:
		path = "/api/v1/wishes/" + id + "/draft"
		method = "DELETE"
	}
	d := command.Definition{
		Kind:         "wish." + string(change.Action),
		Method:       method,
		ResourcePath: path,
		ActorKind:    "student",
		Access:       command.Verified,
	}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return discoverWish(ctx, db, id)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		var row entity.Wish
		// 仅目标缺失或归属不符返回404，存储故障交给拥有边界映射为依赖失败。
		if e := tx.DB().WithContext(ctx).Where("id=? AND owner_id=?", id, tx.Actor().ID).Take(&row).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrNotFound
			}
			return e
		}
		if row.DraftDeletedAt != nil && change.Action != DeleteDraft {
			return command.ErrNotFound
		}
		current, failure := readAggregate(ctx, tx.DB(), id, true)
		if failure != nil {
			return failure
		}
		if !manageable(current.owner, current.snapshot, change.Action) {
			return command.ErrForbidden
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		a, e := readAggregate(ctx, tx.DB(), id, false)
		if e != nil {
			return "", e
		}
		var transition Transition
		if change.Action == Edit {
			if content == nil {
				return "", command.ErrInvalid
			}
			plan, failure := s.policy.EditDraft(ctx, rid, tx.Now(), a.owner, a.snapshot, change.ExpectedVersion, change.RefreshExpiry, *content)
			if failure != nil {
				return "", failure
			}
			if e = invalidate(ctx, tx, id, plan.Transition.InvalidateApplications, "wish_content_changed"); e != nil {
				return "", e
			}
			plain, failure := json.Marshal(plan.Content)
			if failure != nil {
				return "", command.ErrInternal
			}
			defer clear(plain)
			envelope, failure := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: id, Version: uint64(plan.Transition.Next.ContentVersion)}, plain)
			if failure != nil {
				return "", command.ErrUnavailable
			}
			cipher, failure := json.Marshal(envelope)
			if failure != nil {
				return "", command.ErrInternal
			}
			if e = tx.DB().WithContext(ctx).Create(&entity.WishVersion{WishID: id, ContentVersion: plan.Transition.Next.ContentVersion, BodyCipher: cipher, IdentityTags: json.RawMessage(`{}`), RewardMethod: plan.Content.RewardMethod, ModerationStatus: "pending", SchoolDetectionVersion: "pending", SchoolDetectionState: "pending"}).Error; e != nil {
				return "", e
			}
			if e = tx.DB().WithContext(ctx).Create(&entity.ContentRegistry{ID: uuid.NewString(), Kind: "wish", EntityID: id, AuthorID: a.row.OwnerID, ContentVersion: plan.Transition.Next.ContentVersion, Status: "pending", SchoolDetectionVersion: "pending", SchoolDetectionState: "pending"}).Error; e != nil {
				return "", e
			}
			if e = tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where("id=?", id).Updates(map[string]any{"category": plan.Content.Category, "campus": plan.Content.Campus}).Error; e != nil {
				return "", e
			}
			transition = plan.Transition
		} else {
			count := 0
			if change.Action == Publish {
				// 同作者全部写操作先持guard；此计数不依赖缓存或分页，也不锁其他心愿造成反向等待。
				var n int64
				if e = tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where("owner_id=? AND draft_deleted_at IS NULL AND (state='matching' OR (state='published' AND expires_at>?))", a.row.OwnerID, tx.Now()).Count(&n).Error; e != nil {
					return "", e
				}
				count = int(n)
			}
			transition, e = s.policy.Plan(rid, tx.Now(), a.owner, a.snapshot, count, change)
			if e != nil {
				return "", e
			}
			if transition.InvalidateReview || transition.Delete {
				if e = invalidate(ctx, tx, id, transition.InvalidateApplications, "wish_deleted"); e != nil {
					return "", e
				}
			}
			if transition.InvalidateApplications && !transition.InvalidateReview && !transition.Delete {
				if e = invalidateApplications(ctx, tx, id, "wish_withdrawn"); e != nil {
					return "", e
				}
			}
			if transition.EnqueueReview {
				taskID := uuid.NewString()
				epoch := a.row.RestrictionEpoch
				task := entity.ModerationTask{PrivacyRecheck: a.snapshot.PrivacyHeld, ID: taskID, ContentID: a.registry.ID, ContentVersion: a.content.ContentVersion, SubjectEpoch: a.account.AuthEpoch, RestrictionEpoch: &epoch, HoldEpoch: a.snapshot.HoldEpoch, Status: "pending", CreatedAt: tx.Now()}
				if e = tx.DB().WithContext(ctx).Create(&task).Error; e != nil {
					return "", e
				}
				if e = tx.DB().WithContext(ctx).Model(&entity.ContentRegistry{}).Where("id=?", a.registry.ID).Updates(map[string]any{"status": "pending", "school_related": nil, "school_detection_state": "pending", "school_detection_version": "pending"}).Error; e != nil {
					return "", e
				}
				if e = tx.DB().WithContext(ctx).Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", id, a.content.ContentVersion).Updates(map[string]any{"review_task_id": taskID, "moderation_status": "pending", "public_body": nil, "identity_tags": json.RawMessage(`{}`), "school_related": nil, "school_detection_state": "pending", "school_detection_version": "pending"}).Error; e != nil {
					return "", e
				}
				if e = durable.Append(ctx, tx.DB(), s.policy.logger, ReviewRoute(), durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: taskID, AggregateVersion: a.content.ContentVersion}); e != nil {
					return "", e
				}
			}
			if transition.Delete {
				// 不删除审计/申请外键指向的技术对象；清除各版本内容并以不可复活标记屏蔽所有入口。
				if e = tx.DB().WithContext(ctx).Model(&entity.WishVersion{}).Where("wish_id=?", id).Updates(map[string]any{"body_cipher": json.RawMessage(`{}`), "public_body": nil, "identity_tags": json.RawMessage(`{}`)}).Error; e != nil {
					return "", e
				}
				if e = tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where("id=?", id).Update("draft_deleted_at", tx.Now()).Error; e != nil {
					return "", e
				}
				if e = tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where("id=?", id).Update("version", transition.Next.Version).Error; e != nil {
					return "", e
				}
				return id, nil
			}
		}
		if e = saveState(ctx, tx, a, transition.Next); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		if ref != id {
			return command.ErrNotFound
		}
		if change.Action == DeleteDraft {
			var n int64
			if e := tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where("id=? AND owner_id=? AND draft_deleted_at IS NOT NULL", ref, tx.Actor().ID).Count(&n).Error; e != nil {
				return e
			}
			if n != 1 {
				return command.ErrConflict
			}
			return nil
		}
		var e error
		out, e = resolveCreated(ctx, tx.DB(), ref, tx.Actor().ID)
		return e
	}
	// 删除后的资源仍存在，版本比较在Apply中进行；有效回执重放先重新授权，不强制旧版本匹配。
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: struct {
		Change Change
		Digest string
	}{change, digest}})
	return out, err
}

func invalidateApplications(ctx context.Context, tx *command.Tx, id, reason string) error {
	db := tx.DB().WithContext(ctx)
	var apps []entity.Application
	if e := db.Where("wish_id=? AND status IN ?", id, []string{"pending_scan", "pending_review", "active"}).Order("id").Find(&apps).Error; e != nil {
		return e
	}
	var tasks, applicationIDs []string
	for _, a := range apps {
		applicationIDs = append(applicationIDs, a.ID)
		if e := CancelApplication(ctx, db, a, "invalidated", reason); e != nil {
			return e
		}
		if a.ReviewTaskID != nil {
			tasks = append(tasks, *a.ReviewTaskID)
		}
	}
	if e := obsoleteWork(ctx, tx, "moderation_task", tasks); e != nil {
		return e
	}
	// 历史领域工作可能直接以申请为目标，升级后仍须永久取消，不能只处理新增案件路由。
	return obsoleteWork(ctx, tx, "application", applicationIDs)
}
