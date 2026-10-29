package moderation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperCrisisSummary 的来源快照不依赖普通内容存续。
type WhisperCrisisSummary struct {
	ID             string     `json:"id"`
	Version        int64      `json:"version"`
	Source         WhisperRef `json:"source"`
	Status         string     `json:"status"`
	HandoffAt      string     `json:"handoff_at"`
	AllowedActions []string   `json:"allowed_actions"`
}

// WhisperTimeline 使用角色标签，避免向处理界面输出后台账号键。
type WhisperTimeline struct {
	At            string  `json:"at"`
	Action        string  `json:"action"`
	OperatorLabel string  `json:"operator_label"`
	Reason        *string `json:"reason"`
}

// WhisperCrisisDetail 允许未实名来源的学号为空，独立证据不得复制学生卡。
type WhisperCrisisDetail struct {
	WhisperCrisisSummary
	Body            string            `json:"body"`
	StudentNumber   *string           `json:"student_number"`
	IdentityStatus  string            `json:"identity_status"`
	EvidenceScopeID string            `json:"evidence_scope_id"`
	Timeline        []WhisperTimeline `json:"timeline"`
	CareDelivery    string            `json:"care_delivery"`
	StaffDelivery   string            `json:"staff_delivery"`
}

func whisperCrisisRow(db *gorm.DB, id string) (c entity.EvidenceCrisisCase, err error) {
	err = db.Where("id=? AND content_type IN ('whisper_post','whisper_comment','whisper_nickname','whisper_capsule')", id).Take(&c).Error
	return c, missingWhisper(err)
}
func whisperCrisisSummary(c entity.EvidenceCrisisCase) WhisperCrisisSummary {
	status := c.Status
	if status == "pending_handling" {
		status = "open"
	}
	if status == "handling" {
		status = "in_progress"
	}
	return WhisperCrisisSummary{c.ID, c.Version, WhisperRef{c.ContentType, *c.SourceEntityID, *c.SourceContentVersion}, status, c.HandoffAt.UTC().Truncate(time.Second).Format(time.RFC3339), []string{}}
}
func whisperCrisisPlan(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	c, e := whisperCrisisRow(db, id)
	if e != nil {
		return p, e
	}
	if c.SourceTaskID != nil {
		p, e = whisperPlan(ctx, db, *c.SourceTaskID)
		if e != nil && !errors.Is(e, command.ErrNotFound) {
			return p, e
		}
	}
	p.Resources = append(p.Resources, command.Resource{Kind: command.CrisisCase, ID: id}, command.Resource{Kind: command.EvidenceCase, ID: c.EvidenceCaseID})
	return p, nil
}

// WhisperCrises 只列出树洞危机元数据，分页主体摘要不包含后台账号。
func (s *Service) WhisperCrises(ctx context.Context, token, rid, cursor string, limit int) (out whisper.Page[WhisperCrisisSummary], err error) {
	defer s.done("whisper.crisis.list", rid, time.Now(), &err)
	if limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	out.Items = []WhisperCrisisSummary{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorizeRoles(ctx, db, token, command.Discovery{}, []string{"crisis_operator", "super_admin"})
		if e != nil {
			return e
		}
		after, e := s.whisperAfter(actor, "crises", cursor)
		if e != nil {
			return e
		}
		var rows []entity.EvidenceCrisisCase
		q := db.Where("content_type IN ('whisper_post','whisper_comment','whisper_nickname','whisper_capsule') AND EXISTS(SELECT 1 FROM evidence.evidence_cases ec WHERE ec.id=crisis_cases.evidence_case_id AND ec.status<>'deletion_pending')")
		if after != "" {
			q = q.Where("id>?", after)
		}
		if e = q.Order("id").Limit(limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > limit {
			rows = rows[:limit]
			v := s.whisperCursor(actor, "crises", rows[len(rows)-1].ID)
			out.NextCursor = &v
		}
		for _, c := range rows {
			out.Items = append(out.Items, whisperCrisisSummary(c))
		}
		return nil
	})
	if err != nil {
		out = whisper.Page[WhisperCrisisSummary]{}
	}
	return
}
func (s *Service) whisperCrisisDetail(ctx context.Context, db *gorm.DB, actor string, c entity.EvidenceCrisisCase) (out WhisperCrisisDetail, err error) {
	var scope entity.EvidenceCase
	if e := db.Where("id=? AND status<>'deletion_pending'", c.EvidenceCaseID).Take(&scope).Error; e != nil {
		return out, missingWhisper(e)
	}
	item, e := evidenceItem(db, c.EvidenceCaseID)
	if e != nil {
		return out, e
	}
	var subject crisisSubject
	if e = decryptEvidence(ctx, s.codec, "crisis_subject", item.ID, item.SubjectCipher, &subject); e != nil {
		return out, e
	}
	out = WhisperCrisisDetail{WhisperCrisisSummary: whisperCrisisSummary(c), IdentityStatus: "not_verified", EvidenceScopeID: c.EvidenceCaseID, Timeline: []WhisperTimeline{}, CareDelivery: "pending", StaffDelivery: "pending"}
	if c.IdentityStatus != nil {
		out.IdentityStatus = *c.IdentityStatus
	}
	if out.IdentityStatus == "verified" {
		out.StudentNumber = &subject.StudentNumber
	}
	if e = decryptEvidence(ctx, s.codec, "crisis_body", item.ID, item.BodyCipher, &out.Body); e != nil {
		return out, e
	}
	if c.Status != "false_positive" {
		out.AllowedActions = []string{"whisperAdminCrisisAct", "whisperAdminCrisisFalsePositive"}
	}
	var actions []entity.EvidenceAction
	if e = db.Where("case_id=?", c.EvidenceCaseID).Order("at,id").Find(&actions).Error; e != nil {
		return out, e
	}
	out.Timeline = append(out.Timeline, WhisperTimeline{c.DetectedAt.UTC().Truncate(time.Second).Format(time.RFC3339), "isolated", "系统", nil})
	for _, a := range actions {
		var reason *string
		if len(a.ReasonCipher) > 0 {
			var plain string
			if e = decryptEvidence(ctx, s.codec, "crisis_reason", a.ID, a.ReasonCipher, &plain); e != nil {
				return out, e
			}
			reason = &plain
		}
		out.Timeline = append(out.Timeline, WhisperTimeline{a.At.UTC().Truncate(time.Second).Format(time.RFC3339), a.Action, "危机处理员", reason})
	}
	var jobs []entity.CrisisDelivery
	if e = db.Where("case_id=?", c.ID).Find(&jobs).Error; e != nil {
		return out, e
	}
	for _, j := range jobs {
		status := j.Status
		if status == "sent" {
			status = "delivered"
		}
		if status != "delivered" && status != "cancelled" {
			status = "pending"
		}
		if j.Kind == "care" {
			out.CareDelivery = status
		} else {
			out.StaffDelivery = status
		}
	}
	return out, crisisAction(ctx, db, s.codec, c, actor, "view", "案件授权查看", time.Now().UTC())
}

// WhisperCrisis 先验证危机角色，再提交独立证据访问轨迹，普通注销不抹去该轨迹。
func (s *Service) WhisperCrisis(ctx context.Context, token, rid, id string) (out WhisperCrisisDetail, err error) {
	defer s.done("whisper.crisis.detail", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorizeRoles(ctx, db, token, command.Discovery{}, []string{"crisis_operator", "super_admin"})
		if e != nil {
			return e
		}
		c, e := whisperCrisisRow(db, id)
		if e != nil {
			return e
		}
		out, e = s.whisperCrisisDetail(ctx, db, actor, c)
		return e
	})
	if err != nil {
		out = WhisperCrisisDetail{}
	}
	return
}

// EscalateWhisperCrisis 依据授权普通案件或精确举报升格；普通审核员不能越域读取危机原文。
func (s *Service) EscalateWhisperCrisis(ctx context.Context, token, key, rid, source, id string, in CrisisCommand) (out WhisperCrisisSummary, err error) {
	defer s.done("whisper.crisis.escalate", rid, time.Now(), &err)
	if !validID(id) || in.ExpectedVersion < 1 || !text(in.Reason, 500) || (source != "review" && source != "report") {
		return out, command.ErrInvalid
	}
	roles := []string{"super_admin"}
	resource := command.Resource{Kind: command.ModerationTask, ID: id}
	if source == "report" {
		roles = []string{"crisis_operator", "super_admin"}
		resource.Kind = command.Report
	}
	taskID := func(db *gorm.DB) (string, error) {
		if source == "review" {
			return id, nil
		}
		v, e := whisperReport(db, id)
		if e != nil {
			return "", e
		}
		r, e := whisperTarget(db, v.TargetKind, v.TargetID)
		if e != nil {
			return "", e
		}
		if v.ContentID == nil || *v.ContentID != r.registry.ID {
			return "", command.ErrNotFound
		}
		if _, e = whisperPublicBody(db, r); e != nil {
			var n int64
			if failure := db.Model(&entity.EvidenceCase{}).Where("kind='crisis' AND source_ref=? AND status<>'deletion_pending'", r.registry.ID).Count(&n).Error; failure != nil {
				return "", failure
			}
			if n != 1 {
				return "", e
			}
		}
		return r.task.ID, nil
	}
	d := command.Definition{Kind: "whisper.crisis.escalate", Method: "POST", ResourcePath: "/api/v1/admin/whispers/" + source + "s/" + id + "/escalate", ActorKind: "admin", AllowedRoles: roles, RequiredExpectations: []string{"expected_version"}, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		tid, e := taskID(db)
		if e != nil {
			return command.Discovery{}, e
		}
		p, e := whisperPlan(ctx, db, tid)
		if source == "report" {
			p.Resources = append(p.Resources, resource)
		}
		return p, e
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error { _, e := taskID(tx.DB().WithContext(ctx)); return e }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		tid, e := taskID(db)
		if e != nil {
			return "", e
		}
		r, e := loadWhisper(db, tid)
		if e != nil {
			return "", e
		}
		if !whisperCurrent(r) {
			return "", command.ErrNotFound
		}
		if source == "review" {
			if _, e = ordinaryWhisper(db, r); e != nil {
				return "", e
			}
		}
		hash := sha256.Sum256([]byte(token))
		var reportID *string
		if source == "report" {
			reportID = &id
		}
		var raw []struct{ Body json.RawMessage }
		if e = db.Raw("SELECT body FROM public.read_whisper_crisis_payload(decode(?,'hex'),?,?)", hex.EncodeToString(hash[:]), tid, reportID).Scan(&raw).Error; e != nil {
			return "", e
		}
		if len(raw) != 1 {
			return "", command.ErrNotFound
		}
		purpose := r.registry.Kind + "_body"
		if r.registry.Kind == "whisper_nickname" {
			purpose = "whisper_nickname"
		}
		var body string
		if e = whisperPlain(ctx, s.codec, purpose, r.ID, r.ContentVersion, raw[0].Body, &body); e != nil {
			return "", e
		}
		ref, e := captureWhisperCrisis(ctx, db, s.codec, r, tx.Now(), body)
		if e != nil {
			return "", e
		}
		c, e := whisperCrisisRow(db, ref)
		if e != nil {
			return "", e
		}
		if c.Status == "false_positive" {
			return "", command.ErrConflict
		}
		if e = crisisAction(ctx, db, s.codec, c, tx.Actor().ID, "detected", in.Reason, tx.Now()); e != nil {
			return "", e
		}
		return ref, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		c, e := whisperCrisisRow(tx.DB().WithContext(ctx), ref)
		if e != nil {
			return e
		}
		out = whisperCrisisSummary(c)
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: resource, Value: in.ExpectedVersion}}})
	return
}

// ActOnWhisperCrisis 结案保留隔离，误报只在当前资源尚存时创建新普通复核。
func (s *Service) ActOnWhisperCrisis(ctx context.Context, token, key, rid, id string, in CrisisCommand, falsePositive bool) (out WhisperCrisisDetail, err error) {
	defer s.done("whisper.crisis.action", rid, time.Now(), &err)
	if !validID(id) || in.ExpectedVersion < 1 || !text(in.Reason, 500) || !falsePositive && !slicesCrisisAction(in.Action) {
		return out, command.ErrInvalid
	}
	suffix := "actions"
	if falsePositive {
		suffix = "false-positive"
	}
	d := command.Definition{Kind: "whisper.crisis.action", Method: "POST", ResourcePath: "/api/v1/admin/whispers/crises/" + id + "/" + suffix, ActorKind: "admin", AllowedRoles: []string{"crisis_operator", "super_admin"}, RequiredExpectations: []string{"expected_version"}, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return whisperCrisisPlan(ctx, db, id)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		c, e := whisperCrisisRow(tx.DB().WithContext(ctx), id)
		if e != nil {
			return e
		}
		var n int64
		e = tx.DB().Model(&entity.EvidenceCase{}).Where("id=? AND status<>'deletion_pending'", c.EvidenceCaseID).Count(&n).Error
		if e == nil && n != 1 {
			return command.ErrNotFound
		}
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		c, e := whisperCrisisRow(db, id)
		if e != nil {
			return "", e
		}
		if c.Status == "false_positive" {
			return "", command.ErrConflict
		}
		status, action := c.Status, in.Action
		if falsePositive {
			action = "false_positive"
			status = "false_positive"
			if e = db.Model(&entity.ContentRestriction{}).Where("kind='crisis' AND source_ref=? AND active", id).Updates(map[string]any{"active": false, "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
			if c.SourceTaskID != nil {
				r, e := loadWhisper(db, *c.SourceTaskID)
				if e != nil && !errors.Is(e, command.ErrNotFound) {
					return "", e
				}
				if e == nil && whisperCurrent(r) {
					if e = hideWhisper(db, r, true, tx.Now()); e != nil {
						return "", e
					}
					if e = queueWhisper(ctx, db, r, WhisperReviewSink{s.logger}, true, tx.Now()); e != nil {
						return "", e
					}
				}
			}
			if e = db.Model(&entity.CrisisDelivery{}).Where("case_id=? AND status IN ('pending','leased')", id).Updates(map[string]any{"status": "cancelled", "lease_id": nil, "lease_until": nil}).Error; e != nil {
				return "", e
			}
		} else {
			switch action {
			case "take_over":
				if c.Status != "pending_handling" {
					return "", command.ErrConflict
				}
				status = "handling"
			case "resolve":
				status = "resolved"
			case "retry_notification":
				result := db.Model(&entity.CrisisDelivery{}).Where("case_id=? AND kind='operator' AND status='pending' AND attempts>0", id).Update("next_at", tx.Now())
				if result.Error != nil {
					return "", result.Error
				}
				if result.RowsAffected != 1 {
					return "", command.ErrConflict
				}
			}
		}
		if e = crisisAction(ctx, db, s.codec, c, tx.Actor().ID, action, in.Reason, tx.Now()); e != nil {
			return "", e
		}
		return id, db.Model(&entity.EvidenceCrisisCase{}).Where("id=?", id).Updates(map[string]any{"status": status, "resolution": action, "handler_id": tx.Actor().ID, "version": gorm.Expr("version+1")}).Error
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		c, e := whisperCrisisRow(tx.DB().WithContext(ctx), ref)
		if e != nil {
			return e
		}
		out, e = s.whisperCrisisDetail(ctx, tx.DB(), tx.Actor().ID, c)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.CrisisCase, ID: id}, Value: in.ExpectedVersion}}})
	return
}
func slicesCrisisAction(s string) bool {
	return s == "take_over" || s == "follow_up" || s == "resolve" || s == "retry_notification"
}
