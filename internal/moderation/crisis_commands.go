package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// CrisisCommand 按路由接受动作或理由，版本总是绑定目标案件，不绑定正文版本。
type CrisisCommand struct {
	Action          string `json:"action,omitempty"`
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Service) crisisPreflight(ctx context.Context, token string) error {
	return s.transaction(ctx, func(db *gorm.DB) error {
		_, e := s.authorizeRoles(db.Statement.Context, db, token, command.Discovery{}, []string{"crisis_operator", "super_admin"})
		return e
	})
}
func crisisSource(db *gorm.DB, c entity.EvidenceCrisisCase) (row current, exists bool, err error) {
	if c.SourceTaskID == nil {
		return row, false, nil
	}
	row, err = load(db, *c.SourceTaskID)
	if errors.Is(err, command.ErrNotFound) {
		return current{}, false, nil
	}
	return row, err == nil, err
}
func crisisPlan(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	c, e := crisisRow(db, id)
	if e != nil {
		return p, e
	}
	if c.SourceTaskID != nil {
		p, e = discover(ctx, db, *c.SourceTaskID)
		if e != nil && !errors.Is(e, command.ErrNotFound) {
			return p, e
		}
	}
	p.Resources = append(p.Resources, command.Resource{Kind: command.CrisisCase, ID: id})
	return p, nil
}

// EscalateCrisis 允许危机角色依据具体审核任务或举报案件判定风险，已公开内容同事务隐藏。
// 不提供任意私聊搜索；举报来源只解析该案已绑定的精确内容。
func (s *Service) EscalateCrisis(ctx context.Context, token, key, rid, source, id string, in CrisisCommand) (out CrisisView, err error) {
	defer func() {
		if err != nil {
			out = CrisisView{}
		}
	}()
	defer s.done("crisis.escalate", rid, time.Now(), &err)
	if !validID(id) || in.ExpectedVersion < 1 || !text(in.Reason, 2000) || !slices.Contains([]string{"task", "report"}, source) {
		return out, command.ErrInvalid
	}
	if e := s.crisisPreflight(ctx, token); e != nil {
		return out, e
	}
	taskID := func(db *gorm.DB) (string, error) {
		if source == "task" {
			return id, nil
		}
		var r entity.Report
		if e := db.Where("id=? AND content_id IS NOT NULL", id).Take(&r).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return "", command.ErrNotFound
			}
			return "", e
		}
		var t entity.ModerationTask
		if e := db.Where("content_id=?", *r.ContentID).Order("created_at DESC,id DESC").Take(&t).Error; e != nil {
			return "", command.ErrNotFound
		}
		return t.ID, nil
	}
	target := command.Resource{Kind: command.ModerationTask, ID: id}
	path := "/api/v1/admin/moderation-tasks/" + id + "/escalate-crisis"
	if source == "report" {
		target.Kind = command.Report
		path = "/api/v1/admin/reports/" + id + "/escalate-crisis"
	}
	d := command.Definition{Kind: "crisis.escalate", Method: "POST", ResourcePath: path, ActorKind: "admin", AllowedRoles: []string{"crisis_operator", "super_admin"}, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		t, e := taskID(db)
		if e != nil {
			return command.Discovery{}, e
		}
		p, e := discover(ctx, db, t)
		if source == "report" {
			p.Resources = append(p.Resources, target)
		}
		return p, e
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		t, e := taskID(tx.DB().WithContext(ctx))
		if e != nil {
			return e
		}
		row, e := load(tx.DB().WithContext(ctx), t)
		if e != nil {
			return e
		}
		if !capturable(row) {
			return command.ErrNotFound
		}
		var n int64
		if e = tx.DB().WithContext(ctx).Model(&entity.ModerationScan{}).Where("task_id=?", t).Count(&n).Error; e != nil {
			return e
		}
		if n != 1 {
			return command.ErrConflict
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		t, e := taskID(db)
		if e != nil {
			return "", e
		}
		row, e := load(db, t)
		if e != nil {
			return "", e
		}
		ref, e := captureCrisis(ctx, db, s.codec, row, tx.Now())
		if e != nil {
			return "", e
		}
		c, e := crisisRow(db, ref)
		if e != nil {
			return "", e
		}
		if c.Status == "false_positive" {
			return "", command.ErrConflict
		}
		if e = crisisAction(ctx, db, s.codec, c, tx.Actor().ID, "detected", in.Reason, tx.Now()); e != nil {
			return "", e
		}
		if e = db.Model(&entity.ModerationTask{}).Where("id=?", t).Updates(map[string]any{"status": "human_required", "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}

		if source == "report" {
			if e = db.Model(&entity.Report{}).Where("id=?", id).Updates(map[string]any{"status": "processing", "handler_id": tx.Actor().ID, "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
		}
		return ref, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		c, e := crisisRow(tx.DB().WithContext(ctx), ref)
		if e == nil {
			out = crisisSummary(c)
		}
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: target, Value: in.ExpectedVersion}}})
	return
}

// ActOnCrisis 持有业务参与者及危机案件锁后提交时间线，误报只转普通审核，不解除其他屏障。
func (s *Service) ActOnCrisis(ctx context.Context, token, key, rid, id string, in CrisisCommand, falsePositive bool) (out CrisisView, err error) {
	defer func() {
		if err != nil {
			out = CrisisView{}
		}
	}()
	defer s.done("crisis.action", rid, time.Now(), &err)
	if !validID(id) || in.ExpectedVersion < 1 || !text(in.Reason, 2000) || (!falsePositive && !slices.Contains([]string{"take_over", "follow_up", "resolve", "retry_notification"}, in.Action)) {
		return out, command.ErrInvalid
	}
	if e := s.crisisPreflight(ctx, token); e != nil {
		return out, e
	}
	suffix := "actions"
	if falsePositive {
		suffix = "resolve-false-positive"
	}
	d := command.Definition{Kind: "crisis." + strings.ReplaceAll(suffix, "-", "_"), Method: "POST", ResourcePath: "/api/v1/admin/crisis-cases/" + id + "/" + suffix, ActorKind: "admin", AllowedRoles: []string{"crisis_operator", "super_admin"}, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return crisisPlan(ctx, db, id)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		c, e := crisisRow(tx.DB().WithContext(ctx), id)
		if e != nil {
			return e
		}
		var v entity.EvidenceCase
		if e = tx.DB().WithContext(ctx).Where("id=? AND status<>'deletion_pending'", c.EvidenceCaseID).Take(&v).Error; e != nil {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		c, e := crisisRow(db, id)
		if e != nil {
			return "", e
		}
		if c.Status == "false_positive" || c.Status == "resolved" {
			return "", command.ErrConflict
		}
		action, status := in.Action, c.Status
		if falsePositive {
			action = "false_positive"
			status = "false_positive"
			if e = s.returnToOrdinary(ctx, db, c); e != nil {
				return "", e
			}
			if e = db.Model(&entity.ContentRestriction{}).Where("source_ref=? AND kind='crisis' AND active", id).Updates(map[string]any{"active": false, "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
			if e = db.Model(&entity.CrisisDelivery{}).Where("case_id=? AND status IN ('pending','leased')", id).Updates(map[string]any{"status": "cancelled", "lease_id": nil, "lease_until": nil}).Error; e != nil {
				return "", e
			}
		} else {
			switch action {
			case "take_over":
				status = "handling"
			case "resolve":
				status = "resolved"
			case "retry_notification":
				if e = db.Model(&entity.CrisisDelivery{}).Where("case_id=? AND kind='operator' AND status='pending'", id).Update("next_at", tx.Now()).Error; e != nil {
					return "", e
				}
			}
		}
		if e = crisisAction(ctx, db, s.codec, c, tx.Actor().ID, action, in.Reason, tx.Now()); e != nil {
			return "", e
		}
		e = db.Model(&entity.EvidenceCrisisCase{}).Where("id=?", id).Updates(map[string]any{"status": status, "handler_id": tx.Actor().ID, "resolution": action, "version": gorm.Expr("version+1")}).Error
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		c, e := crisisRow(tx.DB().WithContext(ctx), ref)
		if e == nil {
			out = crisisSummary(c)
		}
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.CrisisCase, ID: id}, Value: in.ExpectedVersion}}})
	return
}

// returnToOrdinary 复用原分析产生的脱敏候选，重绑新人工任务，不覆盖历史危机扫描。
// 主体、正文或交互已失效时只保留证据，绝不重建普通数据或恢复旧交互。
func (s *Service) returnToOrdinary(ctx context.Context, db *gorm.DB, c entity.EvidenceCrisisCase) error {
	row, exists, e := crisisSource(db, c)
	if e != nil {
		return e
	}
	if !exists || !currentBinding(row) || row.account.Lifecycle != "active" || row.task.SubjectEpoch != row.account.AuthEpoch || row.wish.DraftDeletedAt != nil {
		return nil
	}
	if row.message != nil && (row.message.Status == "cancelled" || row.message.Status == "rejected") {
		return nil
	}
	if row.application != nil && !slices.Contains([]string{"pending_scan", "pending_review", "active", "accepted"}, row.application.Status) {
		return nil
	}
	if row.story != nil && row.story.WithdrawnAt != nil {
		return nil
	}
	var scan entity.ModerationScan
	if e = db.Where("task_id=?", row.task.ID).Take(&scan).Error; e != nil {
		return e
	}
	var env securedata.Envelope
	if json.Unmarshal(scan.ProjectionCipher, &env) != nil {
		return command.ErrUnavailable
	}
	plain, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "moderation_projection", ObjectID: row.task.ID, Version: 1}, env)
	if e != nil {
		return e
	}
	defer clear(plain)
	id := uuid.NewString()
	next, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "moderation_projection", ObjectID: id, Version: 1}, plain)
	if e != nil {
		return e
	}
	scan.TaskID = id
	scan.CrisisHit = false
	scan.OrdinaryHit = true
	scan.ProjectionCipher, e = json.Marshal(next)
	if e != nil {
		return e
	}
	t := row.task
	t.CrisisRecheck = true
	t.ID = id
	t.Version = 1
	t.Status = "human_required"
	t.Decision = nil
	t.ReviewerID = nil
	t.Reason = nil
	t.RevisionSuggestion = nil
	t.CreatedAt = time.Time{}
	// 原代次保持，只有本次隔离自身改变的心愿限制代次可以衔接；其他屏障仍由最终普通审核复核。
	if row.registry.Kind == "wish" {
		epoch := row.wish.RestrictionEpoch
		t.RestrictionEpoch = &epoch
	}
	if e = db.Create(&t).Error; e != nil {
		return e
	}
	if e = db.Create(&scan).Error; e != nil {
		return e
	}
	if e = db.Model(&entity.ModerationTask{}).Where("id=?", row.task.ID).Updates(map[string]any{"status": "obsolete", "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	if e = db.Model(&entity.ContentRegistry{}).Where("id=?", row.registry.ID).Update("status", "pending").Error; e != nil {
		return e
	}
	updates := map[string]any{"review_task_id": id}
	switch row.registry.Kind {
	case "wish":
		updates["moderation_status"] = "human_required"
		updates["public_body"] = nil
		if e = db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", row.wish.ID, row.registry.ContentVersion).Updates(updates).Error; e != nil {
			return e
		}
		if slices.Contains([]string{"pending_review", "withdrawn", "ready", "rejected"}, row.wish.State) {
			return db.Model(&entity.Wish{}).Where("id=?", row.wish.ID).Updates(map[string]any{"state": "pending_review", "version": gorm.Expr("version+1")}).Error
		}
	case "story":
		updates["moderation_status"] = "human_required"
		updates["public_body"] = nil
		return db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", row.wish.ID, row.registry.ContentVersion).Updates(updates).Error
	case "message":
		if row.message.DeliverySeq == nil {
			updates["status"] = "pending_review"
		}
		updates["version"] = gorm.Expr("version+1")
		return db.Model(&entity.Message{}).Where("id=?", row.message.ID).Updates(updates).Error
	case "application":
		if row.application.Status != "accepted" {
			updates["status"] = "pending_review"
		}
		updates["version"] = gorm.Expr("version+1")
		return db.Model(&entity.Application{}).Where("id=?", row.application.ID).Updates(updates).Error
	}
	return nil
}
