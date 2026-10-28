package safety

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

type operation struct {
	method, path, actor string
	access              command.Access
	resource            command.ResourceKind
}

func spec(op, id string) (operation, bool) {
	switch op {
	case "block":
		return operation{"POST", "/api/v1/me/blocks", "student", command.Ordinary, ""}, true
	case "unblock":
		return operation{"DELETE", "/api/v1/me/blocks/" + id, "student", command.Ordinary, ""}, true
	case "report":
		return operation{"POST", "/api/v1/reports", "student", command.Ordinary, ""}, true
	case "appeal":
		return operation{"POST", "/api/v1/me/appeals", "student", command.Restricted, ""}, true
	case "display", "ack":
		return operation{"POST", "/api/v1/me/warnings/" + id + "/" + op, "student", command.Restricted, command.Penalty}, true
	case "report_decision":
		return operation{"POST", "/api/v1/admin/reports/" + id + "/decision", "admin", "", command.Report}, true
	case "penalty":
		return operation{"POST", "/api/v1/admin/users/" + id + "/penalties", "admin", "", command.Account}, true
	case "appeal_decision":
		return operation{"POST", "/api/v1/admin/appeals/" + id + "/decision", "admin", "", command.Appeal}, true
	case "offline", "release":
		return operation{"POST", "/api/v1/admin/contents/" + id, "admin", "", ""}, true
	case "exception_exit":
		return operation{"POST", "/api/v1/wishes/" + id + "/exception-exit", "student", command.Ordinary, command.Wish}, true
	}
	return operation{}, false
}
func (s *Service) validate(op, id string, in Input) error {
	if _, ok := spec(op, id); !ok {
		return command.ErrInvalid
	}
	switch op {
	case "block":
		if !validID(in.TargetUserID) {
			return command.ErrInvalid
		}
	case "report":
		if !validID(in.ID) || !slices.Contains([]string{"wish", "story", "application", "message"}, in.TargetKind) || !s.text(in.TypeCode, true) || !s.text(in.Description, false) {
			return command.ErrInvalid
		}
	case "appeal":
		if !validID(in.PenaltyID) || !s.text(in.Body, true) {
			return command.ErrInvalid
		}
	default:
		if !validID(id) || in.ExpectedVersion < 1 {
			return command.ErrInvalid
		}
		if op == "offline" || op == "release" {
			if !slices.Contains([]string{"wish", "story", "application", "message"}, in.TargetKind) || !s.text(in.Reason, true) {
				return command.ErrInvalid
			}
		}
		switch op {
		case "penalty":
			if !validID(in.ViolationRef) || !validID(in.ContentRef) || in.ContentVersion < 1 || !slices.Contains([]string{"minor", "general", "severe"}, in.Level) || !s.text(in.Reason, true) {
				return command.ErrInvalid
			}
		case "report_decision":
			if !slices.Contains([]string{"confirmed", "dismissed"}, in.Decision) || !s.text(in.Reason, true) {
				return command.ErrInvalid
			}
		case "appeal_decision":
			if !slices.Contains([]string{"uphold", "revoke"}, in.Decision) || !s.text(in.Reason, true) {
				return command.ErrInvalid
			}
		}
	}
	return nil
}

// Execute 只接受固定命令，取锁、重发现、会话复核、幂等及版本校验统一由框架执行。
// 回执只保存对象引用；失败（包括提前确认警告）回滚，不占用幂等键。
func (s *Service) Execute(ctx context.Context, token, key, rid, op, id string, in Input) (out any, err error) {
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	defer s.done(op, rid, time.Now(), &err)
	if op == "block" || op == "unblock" {
		if e := s.LegacyBlocks(ctx, token); e != nil {
			return nil, e
		}
	}
	if e := s.validate(op, id, in); e != nil {
		return nil, e
	}
	o, _ := spec(op, id)

	if len(key) < 1 || len(key) > 128 {
		return nil, command.ErrInvalid
	}
	// 在发现对象之前检查主体类别与当前权限，避免无权后台账号通过不同发现错误枚举案件。
	// 命令框架取得完整锁后仍会重新授权，预检不代替最终复核。
	if e := s.read(ctx, token, o.actor == "admin", func(db *gorm.DB, actor string, now time.Time) error {
		if o.actor == "student" && o.access != command.Restricted {
			var n int64
			if e := db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", actor, now, now).Count(&n).Error; e != nil {
				return e
			}
			if n > 0 {
				return command.ErrForbidden
			}
		}
		return nil
	}); e != nil {
		return nil, e
	}
	if op == "offline" || op == "release" {
		suffix := "offline"
		if op == "release" {
			suffix = "release-restriction"
		}
		o.path = "/api/v1/admin/contents/" + in.TargetKind + "/" + id + "/" + suffix
		o.resource = map[string]command.ResourceKind{"wish": command.Wish, "story": command.Wish, "application": command.Application, "message": command.Message}[in.TargetKind]
	}
	d := command.Definition{Kind: "safety." + op, Method: o.method, ResourcePath: o.path, ActorKind: o.actor, Access: o.access}
	if o.actor == "admin" {
		d.AllowedRoles = []string{"review_operator", "crisis_operator", "super_admin"}
	}
	var checks []command.Expectation
	if o.resource != "" {
		d.RequiredExpectations = []string{"expected_version"}
		checks = []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: o.resource, ID: id}, Value: in.ExpectedVersion}}
	}
	d.Discover = func(ctx context.Context, db *gorm.DB, actor command.Subject) (command.Discovery, error) {
		return s.discover(ctx, db.WithContext(ctx), actor, op, id, in)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		if op == "block" || op == "unblock" {
			if e := tx.DB().WithContext(ctx).Exec("SELECT pg_advisory_xact_lock_shared(624109175)").Error; e != nil {
				return e
			}
			if e := blockpolicy.Legacy(ctx, tx.DB()); e != nil {
				return e
			}
		}
		return s.authorize(ctx, tx, op, id, in)
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) { return s.apply(ctx, tx, op, id, in) }
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		v, e := s.resolve(ctx, tx, op, id, ref, in)
		if e == nil {
			out = v
		}
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: checks})
	return
}
func (s *Service) discover(ctx context.Context, db *gorm.DB, a command.Subject, op, id string, in Input) (p command.Discovery, err error) {
	switch op {
	case "block":
		return accountPlan(ctx, db, a.ID, in.TargetUserID)
	case "unblock":
		return command.Discovery{Accounts: []string{a.ID, id}}, nil
	case "offline", "release":
		c, e := targetRegistry(db, in.TargetKind, id)
		if e != nil {
			return p, e
		}
		return contentPlan(ctx, db, c)
	case "report":
		c, e := targetRegistry(db, in.TargetKind, in.ID)
		if e != nil {
			return p, e
		}
		return contentPlan(ctx, db, c)
	case "display", "ack", "appeal":
		pid := id
		if op == "appeal" {
			pid = in.PenaltyID
		}
		v, e := penalty(db, pid)
		if e != nil {
			return p, e
		}
		return command.Discovery{Accounts: []string{v.AccountID}, Resources: []command.Resource{{Kind: command.Penalty, ID: pid}}}, nil
	case "report_decision":
		r, e := report(db, id)
		if e != nil {
			return p, e
		}
		if r.ContentID == nil {
			return p, command.ErrNotFound
		}
		c, e := registry(db, *r.ContentID)
		if e != nil {
			return p, e
		}
		p, e = contentPlan(ctx, db, c)
		p.Resources = append(p.Resources, command.Resource{Kind: command.Report, ID: id})
		return p, e
	case "penalty":
		p, err = accountPlan(ctx, db, id)
		if err != nil {
			return
		}
		c, e := registry(db, in.ContentRef)
		if e != nil {
			return p, e
		}
		q, e := contentPlan(ctx, db, c)
		if e != nil {
			return p, e
		}
		p = merge(p, q)
		var rs []entity.Report
		if e = db.Where("id=?", in.ViolationRef).Find(&rs).Error; e != nil {
			return p, e
		}
		if len(rs) > 0 {
			p.Resources = append(p.Resources, command.Resource{Kind: command.Report, ID: in.ViolationRef})
		} else {
			var t entity.ModerationTask
			if e = db.Where("id=?", in.ViolationRef).Take(&t).Error; e != nil {
				return p, found(e)
			}
			p.Resources = append(p.Resources, command.Resource{Kind: command.ModerationTask, ID: t.ID})
		}
		return p, nil
	case "appeal_decision":
		v, e := appeal(db, id)
		if e != nil {
			return p, e
		}
		p, err = accountPlan(ctx, db, v.AccountID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.Penalty, ID: v.PenaltyID}, command.Resource{Kind: command.Appeal, ID: id})
		return
	case "exception_exit":
		return wish.DiscoverInteraction(ctx, db, id)
	}
	return p, command.ErrInvalid
}
func (s *Service) authorize(ctx context.Context, tx *command.Tx, op, id string, in Input) error {
	db := tx.DB().WithContext(ctx)
	actor := tx.Actor().ID
	switch op {
	case "block":
		if actor == in.TargetUserID {
			return command.ErrInvalid
		}
		g, ok := tx.Guard(in.TargetUserID)
		if !ok || g.Lifecycle != "active" {
			return command.ErrNotFound
		}
	case "unblock":
		if actor == id {
			return command.ErrInvalid
		}
	case "report":
		c, e := targetRegistry(db, in.TargetKind, in.ID)
		if e != nil {
			return e
		}
		return visible(ctx, db, c, actor, tx.Now())
	case "display", "ack", "appeal":
		pid := id
		if op == "appeal" {
			pid = in.PenaltyID
		}
		p, e := penalty(db, pid)
		if e != nil {
			return e
		}
		if p.AccountID != actor || (op != "appeal" && p.ActionKind != "warning") {
			return command.ErrNotFound
		}
	case "offline", "release":
		c, e := targetRegistry(db, in.TargetKind, id)
		if e != nil {
			return e
		}
		return ordinaryCase(db, c.ID)
	case "report_decision":
		r, e := report(db, id)
		if e != nil {
			return e
		}
		if r.ContentID == nil {
			return command.ErrNotFound
		}
		return ordinaryCase(db, *r.ContentID)
	case "penalty":
		c, e := registry(db, in.ContentRef)
		if e != nil {
			return e
		}
		if c.AuthorID != id || c.ContentVersion != in.ContentVersion {
			return command.ErrNotFound
		}
		g, ok := tx.Guard(id)
		if !ok || g.Lifecycle != "active" {
			return command.ErrNotFound
		}
		if e = ordinaryCase(db, c.ID); e != nil {
			return e
		}
		var r entity.Report
		e = db.Where("id=? AND content_id=? AND status='resolved'", in.ViolationRef, c.ID).Take(&r).Error
		if e == nil {
			return nil
		}
		if e != gorm.ErrRecordNotFound {
			return e
		}
		var t entity.ModerationTask
		return found(db.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.moderation_scans s ON s.task_id=t.id").Where("t.id=? AND t.content_id=? AND t.content_version=? AND t.status='rejected' AND t.decision='rejected' AND s.ordinary_hit AND NOT s.crisis_hit", in.ViolationRef, c.ID, in.ContentVersion).Take(&t).Error)
	case "appeal_decision":
		a, e := appeal(db, id)
		if e != nil {
			return e
		}
		p, e := penalty(db, a.PenaltyID)
		if e != nil {
			return e
		}
		if p.ContentID == nil {
			return command.ErrNotFound
		}
		return ordinaryCase(db, *p.ContentID)
	case "exception_exit":
		var w entity.Wish
		if e := db.Where("id=? AND draft_deleted_at IS NULL", id).Take(&w).Error; e != nil {
			return found(e)
		}
		if actor == w.OwnerID {
			return nil
		}
		var n int64
		if e := db.Model(&entity.Match{}).Where("wish_id=? AND helper_id=?", id, actor).Count(&n).Error; e != nil {
			return e
		}
		if n == 0 {
			return command.ErrNotFound
		}
	}
	return nil
}
func (s *Service) apply(ctx context.Context, tx *command.Tx, op, id string, in Input) (string, error) {
	db := tx.DB().WithContext(ctx)
	actor := tx.Actor().ID
	switch op {
	case "block":
		var n int64
		if e := db.Model(&entity.Block{}).Where("owner_id=? AND target_id=?", actor, in.TargetUserID).Count(&n).Error; e != nil {
			return "", e
		}
		if n == 0 {
			// 使用账号单调版本为每次新建分配不同代次，删除后重拉黑不能被旧DELETE版本解除。
			guard, _ := tx.Guard(actor)
			if e := db.Model(&entity.Account{}).Where("id=?", actor).Update("version", gorm.Expr("version+1")).Error; e != nil {
				return "", e
			}
			if e := db.Create(&entity.Block{OwnerID: actor, TargetID: in.TargetUserID, Version: guard.Version + 1}).Error; e != nil {
				return "", e
			}
			if e := cancelRelationships(ctx, tx, actor, in.TargetUserID); e != nil {
				return "", e
			}
		}
		return in.TargetUserID, nil
	case "unblock":
		var b entity.Block
		if e := db.Where("owner_id=? AND target_id=?", actor, id).Take(&b).Error; e != nil {
			return "", found(e)
		}
		if b.Version != in.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		return id, db.Where("owner_id=? AND target_id=?", actor, id).Delete(&entity.Block{}).Error
	case "report":
		var n int64
		if e := db.Model(&entity.ReportType{}).Where("code=? AND published", in.TypeCode).Count(&n).Error; e != nil {
			return "", e
		}
		if n != 1 {
			return "", command.ErrInvalid
		}
		c, e := targetRegistry(db, in.TargetKind, in.ID)
		if e != nil {
			return "", e
		}
		r := entity.Report{ID: uuid.NewString(), ReporterID: actor, TargetKind: c.Kind, TargetID: c.EntityID, TypeCode: in.TypeCode, ContentID: &c.ID, CreatedAt: tx.Now()}
		if in.Description != "" {
			r.DescriptionCipher, e = s.encrypt(ctx, "report_description", r.ID, in.Description)
			if e != nil {
				return "", e
			}
		}
		return r.ID, db.Create(&r).Error
	case "appeal":
		p, e := penalty(db, in.PenaltyID)
		if e != nil {
			return "", e
		}
		if p.RevokedAt != nil {
			return "", command.ErrConflict
		}
		a := entity.Appeal{ID: uuid.NewString(), AccountID: actor, PenaltyID: p.ID, CreatedAt: tx.Now()}
		a.BodyCipher, e = s.encrypt(ctx, "appeal_body", a.ID, in.Body)
		if e != nil {
			return "", e
		}
		return a.ID, db.Create(&a).Error
	case "display", "ack":
		p, e := penalty(db, id)
		if e != nil {
			return "", e
		}
		if p.RevokedAt != nil {
			return "", command.ErrConflict
		}
		updates := map[string]any{}
		if op == "display" && p.FirstDisplayedAt == nil {
			updates["first_displayed_at"] = tx.Now()
			updates["confirmable_at"] = tx.Now().Add(30 * time.Second)
		}
		if op == "ack" && p.AcknowledgedAt == nil {
			if !confirmable(p, tx.Now()) {
				return "", command.ErrWarningWait
			}
			updates["acknowledged_at"] = tx.Now()
		}
		if len(updates) > 0 {
			updates["version"] = gorm.Expr("version+1")
			if e = db.Model(&entity.Penalty{}).Where("id=?", id).Updates(updates).Error; e != nil {
				return "", e
			}
		}
		return id, nil
	case "report_decision":
		r, e := report(db, id)
		if e != nil {
			return "", e
		}
		if r.Status != "pending" && r.Status != "processing" {
			return "", command.ErrConflict
		}
		status := "rejected"
		if in.Decision == "confirmed" {
			status = "resolved"
		}
		if e = db.Model(&entity.Report{}).Where("id=?", id).Updates(map[string]any{"status": status, "handler_id": actor, "decision_reason": in.Reason, "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		return id, audit(db, actor, id, "report.decision")
	case "offline", "release":
		return s.restrict(ctx, tx, op, id, in)
	case "penalty":
		return s.impose(ctx, tx, id, in)
	case "appeal_decision":
		a, e := appeal(db, id)
		if e != nil {
			return "", e
		}
		if a.Status != "pending" {
			return "", command.ErrConflict
		}
		status := "rejected"
		if in.Decision == "revoke" {
			status = "accepted"
			if e = db.Model(&entity.Penalty{}).Where("id=? AND revoked_at IS NULL", a.PenaltyID).Updates(map[string]any{"revoked_at": tx.Now(), "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
		}
		if e = db.Model(&entity.Appeal{}).Where("id=?", id).Updates(map[string]any{"status": status, "decision_reason": in.Reason, "handler_id": actor, "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		if e = audit(db, actor, id, "appeal.decision"); e != nil {
			return "", e
		}
		return id, notify(db, a.AccountID, "appeal_result", id)
	case "exception_exit":
		return exceptionExit(ctx, tx, id)
	}
	return "", command.ErrInvalid
}
func warning(p entity.Penalty, now time.Time) Warning {
	remaining := 30
	if p.ConfirmableAt != nil {
		remaining = int(math.Ceil(p.ConfirmableAt.Sub(now).Seconds()))
		if remaining < 0 {
			remaining = 0
		}
		if remaining > 30 {
			remaining = 30
		}
	}
	return Warning{p.ID, p.Version, "请遵守社区规则，违规内容已移除", p.FirstDisplayedAt, p.ConfirmableAt, now, remaining, p.AcknowledgedAt}
}
func (s *Service) resolve(ctx context.Context, tx *command.Tx, op, id, ref string, in Input) (any, error) {
	db := tx.DB().WithContext(ctx)
	actor := tx.Actor().ID
	switch op {
	case "block":
		var n int64
		e := db.Model(&entity.Block{}).Where("owner_id=? AND target_id=?", actor, ref).Count(&n).Error
		if e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, command.ErrConflict
		}
		return map[string]any{"target_user_id": ref, "blocked": true}, nil
	case "unblock":
		var n int64
		e := db.Model(&entity.Block{}).Where("owner_id=? AND target_id=?", actor, id).Count(&n).Error
		if e != nil {
			return nil, e
		}
		if n != 0 {
			return nil, command.ErrConflict
		}
		return nil, nil
	case "report", "report_decision":
		r, e := report(db, ref)
		if e != nil {
			return nil, e
		}
		if op == "report" && r.ReporterID != actor {
			return nil, command.ErrNotFound
		}
		status := r.Status
		if op == "report" {
			status = "pending"
		}
		return Summary{ID: r.ID, Version: r.Version, Status: status}, nil
	case "display", "ack":
		p, e := penalty(db, ref)
		if e != nil {
			return nil, e
		}
		if p.AccountID != actor || p.ActionKind != "warning" || p.RevokedAt != nil {
			return nil, command.ErrNotFound
		}
		return warning(p, tx.Now()), nil
	case "appeal", "appeal_decision":
		a, e := appeal(db, ref)
		if e != nil {
			return nil, e
		}
		if op == "appeal" && a.AccountID != actor {
			return nil, command.ErrNotFound
		}
		if op == "appeal" {
			return Appeal{a.ID, a.Version, "pending", nil}, nil
		}
		return Appeal{a.ID, a.Version, a.Status, a.DecisionReason}, nil
	case "penalty":
		p, e := penalty(db, ref)
		if e != nil {
			return nil, e
		}
		if p.AccountID != id {
			return nil, command.ErrNotFound
		}
		status := "active"
		if p.RevokedAt != nil {
			status = "revoked"
		} else if p.EndsAt != nil && !tx.Now().Before(*p.EndsAt) {
			status = "expired"
		}
		return Summary{ID: p.ID, Version: p.Version, Status: status}, nil
	case "offline", "release":
		c, e := targetRegistry(db, in.TargetKind, ref)
		if e != nil {
			return nil, e
		}
		return restrictionSummary(db, c)
	case "exception_exit":
		var w entity.Wish
		e := db.Where("id=?", ref).Take(&w).Error
		if e != nil {
			return nil, found(e)
		}
		return map[string]any{"id": ref, "version": w.Version, "content_version": w.CurrentContentVersion, "current_state": w.State, "allowed_actions": []string{}}, nil
	}
	return nil, command.ErrInvalid
}

func confirmable(p entity.Penalty, now time.Time) bool {
	return p.ConfirmableAt != nil && !now.Before(*p.ConfirmableAt)
}
