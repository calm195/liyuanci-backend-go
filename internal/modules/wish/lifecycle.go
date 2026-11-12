package wish

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/modules/contentreview"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// UpdateInput 始终是完整内容替换；刷新期限也必须形成新版本并重新送审。
type UpdateInput struct {
	ContentInput
	ExpectedVersion int64 `json:"expected_version"`
	RefreshExpiry   bool  `json:"refresh_expiry"`
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
	return s.mutate(
		ctx,
		token,
		key,
		rid,
		id,
		Change{Action: Edit, ExpectedVersion: input.ExpectedVersion, RefreshExpiry: input.RefreshExpiry},
		&input.ContentInput,
		"",
	)
}

// Submit 持久接收指定内容版本的送审，返回待审而不是公开成功。
func (s *Service) Submit(ctx context.Context, token, key, rid, id string, version, contentVersion int64) (CreatedDraft, error) {
	return s.mutate(
		ctx,
		token,
		key,
		rid,
		id,
		Change{Action: SubmitReview, ExpectedVersion: version, ExpectedContentVersion: contentVersion},
		nil,
		"",
	)
}

// SubmitForPublication 持久记录作者明确发布意愿，通过审核后自动上架。
func (s *Service) SubmitForPublication(ctx context.Context, token, key, rid, id string, version, contentVersion int64) (CreatedDraft, error) {
	return s.mutate(ctx, token, key, rid, id, Change{Action: SubmitReview, ExpectedVersion: version, ExpectedContentVersion: contentVersion, PublishIntent: true}, nil, "")
}

// Publish 在同作者guard下复核精确审核与期限，不限制活跃心愿数量。
func (s *Service) Publish(ctx context.Context, token, key, rid, id string, version int64) (CreatedDraft, error) {
	return s.mutate(ctx, token, key, rid, id, Change{Action: Publish, ExpectedVersion: version}, nil, "")
}

// Withdraw 取消待审发布意愿或撤下当前心愿，并原子作废审核和未终结申请。
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
			transition, e = s.editWishDraft(ctx, tx, rid, id, change, content, a)
			if e != nil {
				return "", e
			}
		} else {
			if change.Action == Publish {
				if e = validateBoundImages(ctx, tx.DB(), id, a.row.CurrentContentVersion); e != nil {
					return "", e
				}
			}
			transition, e = s.policy.Plan(rid, tx.Now(), a.owner, a.snapshot, 0, change)
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
				task := entity.ModerationTask{
					PrivacyRecheck:   a.snapshot.PrivacyHeld,
					ID:               taskID,
					ContentID:        a.registry.ID,
					ContentVersion:   a.content.ContentVersion,
					SubjectEpoch:     a.account.AuthEpoch,
					RestrictionEpoch: &epoch,
					HoldEpoch:        a.snapshot.HoldEpoch,
					Status:           "pending",
					CreatedAt:        tx.Now(),
				}
				if e = tx.DB().WithContext(ctx).Create(&task).Error; e != nil {
					return "", e
				}
				if e = tx.DB().WithContext(ctx).Model(&entity.ContentRegistry{}).Where("id=?", a.registry.ID).Updates(map[string]any{
					"status":                   "pending",
					"school_related":           nil,
					"school_detection_state":   "pending",
					"school_detection_version": "pending",
				}).Error; e != nil {
					return "", e
				}
				if e = tx.DB().WithContext(ctx).Model(&entity.WishVersion{}).Where(
					"wish_id=? AND content_version=?",
					id,
					a.content.ContentVersion,
				).Updates(map[string]any{
					"review_task_id":           taskID,
					"moderation_status":        "pending",
					"public_body":              nil,
					"public_title":             nil,
					"public_reward_text":       nil,
					"identity_tags":            json.RawMessage(`{}`),
					"school_related":           nil,
					"school_detection_state":   "pending",
					"school_detection_version": "pending",
				}).Error; e != nil {
					return "", e
				}
				if e = durable.Append(
					ctx,
					tx.DB(),
					s.policy.logger,
					contentreview.Route(),
					durable.Draft{
						WorkID:           uuid.NewString(),
						OperationID:      uuid.NewString(),
						EventID:          uuid.NewString(),
						AggregateID:      taskID,
						AggregateVersion: a.content.ContentVersion,
					},
				); e != nil {
					return "", e
				}
			}
			if transition.Delete {
				// 不删除审计/申请外键指向的技术对象；清除各版本内容并以不可复活标记屏蔽所有入口。
				if e = tx.DB().WithContext(ctx).Model(&entity.WishVersion{}).Where("wish_id=?", id).Updates(map[string]any{
					"body_cipher":        json.RawMessage(`{}`),
					"public_body":        nil,
					"public_title":       nil,
					"public_reward_text": nil,
					"identity_tags":      json.RawMessage(`{}`),
				}).Error; e != nil {
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
			if e := tx.DB().WithContext(ctx).Model(&entity.Wish{}).Where(
				"id=? AND owner_id=? AND draft_deleted_at IS NOT NULL",
				ref,
				tx.Actor().ID,
			).Count(&n).Error; e != nil {
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

// editWishDraft 保存独立加密新稿及内容索引，旧审核与申请在同一事务内失效。
// 此处只返回策略计算的迁移结果，心愿聚合状态仍由调用者统一落库。
func (s *Service) editWishDraft(ctx context.Context, tx *command.Tx, rid, id string, change Change, content *ContentInput, a aggregate) (Transition, error) {
	if content == nil {
		return Transition{}, command.ErrInvalid
	}
	// 省略期限不表示重置默认值；旧客户端不能把超出其范围的期限静默截短。
	if content.DurationDays == nil {
		duration := int(a.row.DurationDays)
		content.DurationDays = &duration
	} else if !identitySupported(ctx) && a.row.DurationDays > 30 && *content.DurationDays != int(a.row.DurationDays) {
		return Transition{}, command.ErrConflict
	}
	// 旧客户端省略新增字段时保留当前图片及身份模式，避免编辑静默抹除。
	if content.IdentityMode == "" {
		content.IdentityMode = a.row.IdentityMode
	}
	if a.row.PublishedAt != nil && content.IdentityMode != a.row.IdentityMode {
		return Transition{}, command.ErrConflict
	}
	if content.ImageIDs == nil {
		var refs []entity.WishVersionImage
		if err := tx.DB().WithContext(ctx).Where("wish_id=? AND content_version=?", id, a.row.CurrentContentVersion).Order("position").Find(&refs).Error; err != nil {
			return Transition{}, err
		}
		content.ImageIDs = []string{}
		for _, ref := range refs {
			content.ImageIDs = append(content.ImageIDs, ref.ImageID)
		}
	}
	plan, err := s.policy.EditDraft(
		ctx,
		rid,
		tx.Now(),
		a.owner,
		a.snapshot,
		change.ExpectedVersion,
		change.RefreshExpiry,
		*content,
	)
	if err != nil {
		return Transition{}, err
	}
	if err = invalidate(ctx, tx, id, plan.Transition.InvalidateApplications, "wish_content_changed"); err != nil {
		return Transition{}, err
	}
	plain, err := json.Marshal(plan.Content)
	if err != nil {
		return Transition{}, command.ErrInternal
	}
	defer clear(plain)
	envelope, err := s.codec.Encrypt(ctx, securedata.Binding{
		Purpose: "wish_content", ObjectID: id, Version: uint64(plan.Transition.Next.ContentVersion),
	}, plain)
	if err != nil {
		return Transition{}, command.ErrUnavailable
	}
	cipher, err := json.Marshal(envelope)
	if err != nil {
		return Transition{}, command.ErrInternal
	}
	db := tx.DB().WithContext(ctx)
	if err = db.Create(&entity.WishVersion{
		WishID:                 id,
		ContentVersion:         plan.Transition.Next.ContentVersion,
		BodyCipher:             cipher,
		IdentityTags:           json.RawMessage(`{}`),
		RewardMethod:           plan.Content.RewardMethod,
		ModerationStatus:       "pending",
		SchoolDetectionVersion: "pending",
		SchoolDetectionState:   "pending",
	}).Error; err != nil {
		return Transition{}, err
	}
	if err = db.Create(&entity.ContentRegistry{
		ID:                     uuid.NewString(),
		Kind:                   "wish",
		EntityID:               id,
		AuthorID:               a.row.OwnerID,
		ContentVersion:         plan.Transition.Next.ContentVersion,
		Status:                 "pending",
		SchoolDetectionVersion: "pending",
		SchoolDetectionState:   "pending",
	}).Error; err != nil {
		return Transition{}, err
	}
	if err = db.Model(&entity.Wish{}).Where("id=?", id).Updates(map[string]any{
		"category":      plan.Content.Category,
		"campus":        plan.Content.Campus,
		"identity_mode": plan.Content.IdentityMode,
	}).Error; err != nil {
		return Transition{}, err
	}
	if err = bindWishImages(ctx, db, id, plan.Transition.Next.ContentVersion, a.row.OwnerID, plan.Content.ImageIDs); err != nil {
		return Transition{}, err
	}
	return plan.Transition, nil
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
		if e := db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: a.ApplicantID, Type: "application_invalidated", TargetRef: a.ID}).Error; e != nil {
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
