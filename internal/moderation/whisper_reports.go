package moderation

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperReportInput 只能举报当前可见的公开文字，不接受账号或昵称来源。
type WhisperReportInput struct {
	Source      WhisperRef `json:"source"`
	TypeCode    string     `json:"type_code"`
	Description string     `json:"description,omitempty"`
}

// WhisperReportSummary 隔离匿名来源与举报人身份。
type WhisperReportSummary struct {
	ID             string     `json:"id"`
	Version        int64      `json:"version"`
	Source         WhisperRef `json:"source"`
	Status         string     `json:"status"`
	CreatedAt      string     `json:"created_at"`
	AllowedActions []string   `json:"allowed_actions"`
}

// WhisperReportDetail 只包含投诉说明和当前必要公开正文，不返回私密身份输入。
type WhisperReportDetail struct {
	WhisperReportSummary
	TypeCode    string  `json:"type_code"`
	Description *string `json:"description"`
	Body        string  `json:"body"`
}

// WhisperReportDecision 绑定举报案件版本。
type WhisperReportDecision struct {
	ExpectedVersion int64  `json:"expected_version"`
	Decision        string `json:"decision"`
	Reason          string `json:"reason"`
}

// WhisperReportAck 仅确认受理，不泄漏后台处置结果。
type WhisperReportAck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func whisperReport(db *gorm.DB, id string) (v entity.Report, err error) {
	err = db.Where("id=? AND target_kind IN ('whisper_post','whisper_comment')", id).Take(&v).Error
	return v, missingWhisper(err)
}
func whisperReportSummary(db *gorm.DB, v entity.Report) (out WhisperReportSummary, err error) {
	var c entity.ContentRegistry
	if v.ContentID == nil {
		return out, command.ErrNotFound
	}
	if err = db.Where("id=?", *v.ContentID).Take(&c).Error; err != nil {
		return
	}
	state := v.Status
	if state == "resolved" {
		state = "confirmed"
	}
	if state == "rejected" {
		state = "dismissed"
	}
	if state == "processing" {
		state = "pending"
	}
	return WhisperReportSummary{v.ID, v.Version, WhisperRef{c.Kind, c.EntityID, c.ContentVersion}, state, v.CreatedAt.UTC().Truncate(time.Second).Format(time.RFC3339), []string{}}, nil
}
func whisperPublicBody(db *gorm.DB, r whisperSource) (string, error) {
	var rows []struct{ Body string }
	if e := db.Raw("SELECT body FROM public.whisper_public_report_body(?,?)", r.registry.Kind, r.ID).Scan(&rows).Error; e != nil {
		return "", e
	}
	if len(rows) != 1 || rows[0].Body == "" {
		return "", command.ErrNotFound
	}
	return rows[0].Body, nil
}

// CreateWhisperReport 在本人会话及内容参与者锁内验证可见性，不允许自身或过期版本举报。
func (s *Service) CreateWhisperReport(ctx context.Context, token, key, rid string, in WhisperReportInput) (out WhisperReportAck, err error) {
	defer s.done("whisper.report.create", rid, time.Now(), &err)
	if (in.Source.Kind != "whisper_post" && in.Source.Kind != "whisper_comment") || !validID(in.Source.ID) || in.Source.ContentVersion < 1 || !text(in.TypeCode, 32) || len([]rune(in.Description)) > 500 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "whisper.report.create", Method: "POST", ResourcePath: "/api/v1/whispers/reports", ActorKind: "student", Access: command.Ordinary, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		r, e := whisperTarget(db, in.Source.Kind, in.Source.ID)
		if e != nil {
			return command.Discovery{}, e
		}
		return whisperPlan(ctx, db, r.task.ID)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error { return nil }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		r, e := whisperTarget(db, in.Source.Kind, in.Source.ID)
		if e != nil {
			return "", e
		}
		if r.ContentVersion != in.Source.ContentVersion || r.OwnerID == tx.Actor().ID {
			return "", command.ErrNotFound
		}
		if _, e = whisperPublicBody(db, r); e != nil {
			return "", e
		}
		var n int64
		if e = db.Model(&entity.ReportType{}).Where("code=? AND published", in.TypeCode).Count(&n).Error; e != nil {
			return "", e
		}
		if n != 1 {
			return "", command.ErrInvalid
		}
		if e = db.Raw(`SELECT (SELECT count(*) FROM public.penalties WHERE account_id=? AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp()))+(SELECT count(*) FROM public.blocks WHERE (owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?))`, tx.Actor().ID, tx.Actor().ID, r.OwnerID, r.OwnerID, tx.Actor().ID).Scan(&n).Error; e != nil {
			return "", e
		}
		if n > 0 {
			return "", command.ErrForbidden
		}
		v := entity.Report{ID: uuid.NewString(), Version: 1, ReporterID: tx.Actor().ID, ContentID: &r.registry.ID, TargetKind: r.registry.Kind, TargetID: r.ID, TypeCode: in.TypeCode, Status: "pending", CreatedAt: tx.Now()}
		if in.Description != "" {
			v.DescriptionCipher, _, e = encryptEvidence(ctx, s.codec, "report_description", v.ID, in.Description)
			if e != nil {
				return "", e
			}
		}
		return v.ID, db.Create(&v).Error
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		v, e := whisperReport(tx.DB().WithContext(ctx), ref)
		if e != nil {
			return e
		}
		if v.ReporterID != tx.Actor().ID {
			return command.ErrNotFound
		}
		out = WhisperReportAck{v.ID, "pending"}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in)})
	return
}

// WhisperReports 仅返回树洞普通举报元数据；危机隔离后不再向普通工作台列出。
func (s *Service) WhisperReports(ctx context.Context, token, rid, cursor string, limit int) (out whisper.Page[WhisperReportSummary], err error) {
	defer s.done("whisper.report.list", rid, time.Now(), &err)
	if limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	out.Items = []WhisperReportSummary{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorizeRoles(ctx, db, token, command.Discovery{}, whisperGovernanceRoles)
		if e != nil {
			return e
		}
		after, e := s.whisperAfter(actor, "reports", cursor)
		if e != nil {
			return e
		}
		var rows []entity.Report
		q := db.Where("target_kind IN ('whisper_post','whisper_comment') AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE c.kind=reports.target_kind AND c.entity_id=reports.target_id AND x.active AND x.kind='crisis')")
		if after != "" {
			q = q.Where("id>?", after)
		}
		if e = q.Order("id").Limit(limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > limit {
			rows = rows[:limit]
			v := s.whisperCursor(actor, "reports", rows[len(rows)-1].ID)
			out.NextCursor = &v
		}
		for _, v := range rows {
			summary, e := whisperReportSummary(db, v)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, summary)
		}
		return nil
	})
	if err != nil {
		out = whisper.Page[WhisperReportSummary]{}
	}
	return
}

// WhisperReport 访问审计提交之后才返回限定投诉和公开正文；隔离内容不会借举报读取。
func (s *Service) WhisperReport(ctx context.Context, token, rid, id string) (out WhisperReportDetail, err error) {
	defer s.done("whisper.report.detail", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		v, e := whisperReport(db, id)
		if e != nil {
			return e
		}
		r, e := whisperTarget(db, v.TargetKind, v.TargetID)
		if e != nil {
			return e
		}
		p, e := whisperPlan(ctx, db, r.task.ID)
		if e != nil {
			return e
		}
		p.Resources = append(p.Resources, command.Resource{Kind: command.Report, ID: id})
		actor, e := s.authorizeRoles(ctx, db, token, p, whisperGovernanceRoles)
		if e != nil {
			return e
		}
		body, e := whisperPublicBody(db, r)
		if e != nil {
			return e
		}
		summary, e := whisperReportSummary(db, v)
		if e != nil {
			return e
		}
		out = WhisperReportDetail{WhisperReportSummary: summary, TypeCode: v.TypeCode, Body: body}
		if len(v.DescriptionCipher) > 0 {
			var description string
			if e = decryptEvidence(ctx, s.codec, "report_description", id, v.DescriptionCipher, &description); e != nil {
				return e
			}
			out.Description = &description
		}
		if v.Status == "pending" {
			out.AllowedActions = []string{"whisperAdminReportDecide", "whisperAdminReportEscalate"}
		}
		return audit(db, actor, r.OwnerID, "report.view")
	})
	if err != nil {
		out = WhisperReportDetail{}
	}
	return
}

// DecideWhisperReport 只确认举报，不隐式施加账号处罚。
func (s *Service) DecideWhisperReport(ctx context.Context, token, key, rid, id string, in WhisperReportDecision) (out WhisperReportSummary, err error) {
	defer s.done("whisper.report.decide", rid, time.Now(), &err)
	if !validID(id) || in.ExpectedVersion < 1 || !text(in.Reason, 500) || (in.Decision != "confirmed" && in.Decision != "dismissed") {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "whisper.report.decide", Method: "POST", ResourcePath: "/api/v1/admin/whispers/reports/" + id + "/decision", ActorKind: "admin", AllowedRoles: whisperGovernanceRoles, RequiredExpectations: []string{"expected_version"}, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		v, e := whisperReport(db, id)
		if e != nil {
			return command.Discovery{}, e
		}
		r, e := whisperTarget(db, v.TargetKind, v.TargetID)
		if e != nil {
			return command.Discovery{}, e
		}
		p, e := whisperPlan(ctx, db, r.task.ID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.Report, ID: id})
		return p, e
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error { return nil }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		v, e := whisperReport(db, id)
		if e != nil {
			return "", e
		}
		if v.Status != "pending" {
			return "", command.ErrConflict
		}
		r, e := whisperTarget(db, v.TargetKind, v.TargetID)
		if e != nil {
			return "", e
		}
		if _, e = whisperPublicBody(db, r); e != nil {
			return "", e
		}
		return id, db.Model(&entity.Report{}).Where("id=?", id).Updates(map[string]any{"status": map[string]string{"confirmed": "resolved", "dismissed": "rejected"}[in.Decision], "decision_reason": in.Reason, "handler_id": tx.Actor().ID, "version": gorm.Expr("version+1")}).Error
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		v, e := whisperReport(tx.DB().WithContext(ctx), ref)
		if e != nil {
			return e
		}
		out, e = whisperReportSummary(tx.DB(), v)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.Report, ID: id}, Value: in.ExpectedVersion}}})
	return
}
