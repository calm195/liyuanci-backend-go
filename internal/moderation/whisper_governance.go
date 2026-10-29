package moderation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperControl 是当前治理版本，不包含正文或作者账号。
type WhisperControl struct {
	Source         WhisperRef `json:"source"`
	Version        int64      `json:"version"`
	Restricted     bool       `json:"restricted"`
	State          string     `json:"state"`
	AllowedActions []string   `json:"allowed_actions"`
}

// WhisperRestrictionCommand 的解除只移除普通违规限制，重新送审后才可公开。
type WhisperRestrictionCommand struct {
	ExpectedVersion int64  `json:"expected_version"`
	Restricted      bool   `json:"restricted"`
	Reason          string `json:"reason"`
}

// WhisperPenaltyCommand 必须引用已确认的精确举报版本。
type WhisperPenaltyCommand struct {
	ExpectedVersion       int64  `json:"expected_version"`
	ReportID              string `json:"report_id"`
	ExpectedReportVersion int64  `json:"expected_report_version"`
	Level                 string `json:"level"`
	Reason                string `json:"reason"`
}

// WhisperPenaltyResult 只返回处罚技术标识及动作，不暴露被处置账号。
type WhisperPenaltyResult struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	EndsAt *string `json:"ends_at"`
}

var whisperGovernanceRoles = []string{"review_operator", "crisis_operator", "super_admin"}

func whisperTarget(db *gorm.DB, kind, id string) (r whisperSource, err error) {
	if !whisperKind(kind) || !validID(id) {
		return r, command.ErrNotFound
	}
	var t entity.ModerationTask
	q := db.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.kind=?", kind)
	if kind == "whisper_nickname" {
		q = q.Where("c.id=?", id)
	} else {
		q = q.Where("c.entity_id=?", id)
	}
	if err = q.Order("c.content_version DESC,t.created_at DESC,t.id DESC").Take(&t).Error; err != nil {
		return r, missingWhisper(err)
	}
	return loadWhisper(db, t.ID)
}
func whisperResource(r whisperSource) command.Resource {
	return command.Resource{Kind: map[string]command.ResourceKind{"whisper_post": command.WhisperPost, "whisper_comment": command.WhisperComment, "whisper_nickname": command.WhisperNickname, "whisper_capsule": command.WhisperCapsule}[r.registry.Kind], ID: r.ID}
}
func whisperControl(db *gorm.DB, r whisperSource) (out WhisperControl, err error) {
	out = WhisperControl{whisperRef(r), r.Version, false, r.State, []string{"whisperAdminRestrictionSet"}}
	var flags struct{ Restricted, Isolated, Privacy bool }
	err = db.Raw(`SELECT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE c.kind=? AND c.entity_id=? AND x.active) AS restricted,EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE c.kind=? AND c.entity_id=? AND x.active AND x.kind='crisis') AS isolated,EXISTS(SELECT 1 FROM public.privacy_holds WHERE target_kind=? AND target_id=? AND status='active') AS privacy`, r.registry.Kind, r.registry.EntityID, r.registry.Kind, r.registry.EntityID, r.registry.Kind, r.ID).Scan(&flags).Error
	out.Restricted = flags.Restricted
	if r.State == "pending" {
		out.State = "review_pending"
	}
	if r.registry.Kind == "whisper_capsule" && r.State == "approved" {
		out.State = "locked"
		var unlocked bool
		if err = db.Raw("SELECT ?<=clock_timestamp()", r.UnlockAt).Scan(&unlocked).Error; err != nil {
			return
		}
		if unlocked {
			out.State = "unlocked"
		}
	}
	if r.registry.Kind == "whisper_post" && r.State == "approved" {
		out.State = "published"
	}
	if r.Lifecycle != "active" {
		out.State = r.Lifecycle
	}
	if flags.Restricted {
		out.State = "restricted"
	}
	if flags.Isolated {
		out.State = "isolated"
	}
	if flags.Privacy {
		out.State = "privacy_hold"
	}
	if r.registry.Kind == "whisper_post" || r.registry.Kind == "whisper_comment" {
		out.AllowedActions = append(out.AllowedActions, "whisperAdminPenaltyCreate")
	}
	return
}

// WhisperContent 只投影当前资源治理状态，昵称治理版本与偏好版本相互独立。
func (s *Service) WhisperContent(ctx context.Context, token, rid, kind, id string) (out WhisperControl, err error) {
	defer s.done("whisper.content.get", rid, time.Now(), &err)
	err = s.transaction(ctx, func(db *gorm.DB) error {
		if _, e := s.authorizeRoles(ctx, db, token, command.Discovery{}, whisperGovernanceRoles); e != nil {
			return e
		}
		r, e := whisperTarget(db, kind, id)
		if e != nil {
			return e
		}
		out, e = whisperControl(db, r)
		return e
	})
	if err != nil {
		out = WhisperControl{}
	}
	return
}
func queueWhisper(ctx context.Context, db *gorm.DB, r whisperSource, loggerSink WhisperReviewSink, crisis bool, now time.Time) error {
	// 仅从仍存在的当前资源创建新轮次；任何危机或隐私屏障仍由消费者失败关闭。
	if r.Lifecycle != "active" || r.account.Lifecycle != "active" {
		return nil
	}
	epoch := r.RestrictionEpoch + 1
	if r.registry.Kind == "whisper_nickname" {
		epoch = 0
	}
	t := entity.ModerationTask{ID: uuid.NewString(), ContentID: r.registry.ID, ContentVersion: r.ContentVersion, SubjectEpoch: r.account.AuthEpoch, HoldEpoch: r.PrivacyEpoch, RestrictionEpoch: &epoch, Status: "pending", CrisisRecheck: crisis, CreatedAt: now}
	if e := db.Create(&t).Error; e != nil {
		return e
	}
	var e error
	switch r.registry.Kind {
	case "whisper_post":
		e = db.Model(&entity.WhisperPost{}).Where("id=?", r.ID).Updates(map[string]any{"review_state": "scanning", "review_round": gorm.Expr("review_round+1")}).Error
		if e == nil {
			e = db.Model(&entity.WhisperPostVersion{}).Where("content_registry_id=?", r.registry.ID).Updates(map[string]any{"review_state": "scanning", "review_task_id": t.ID}).Error
		}
	case "whisper_comment":
		e = db.Model(&entity.WhisperComment{}).Where("id=?", r.ID).Updates(map[string]any{"review_state": "scanning", "review_round": gorm.Expr("review_round+1")}).Error
		if e == nil {
			e = db.Model(&entity.WhisperCommentVersion{}).Where("content_registry_id=?", r.registry.ID).Updates(map[string]any{"review_state": "scanning", "review_task_id": t.ID}).Error
		}
	case "whisper_nickname":
		e = db.Model(&entity.WhisperNicknameVersion{}).Where("content_registry_id=?", r.ID).Updates(map[string]any{"review_state": "scanning", "review_round": gorm.Expr("review_round+1"), "review_task_id": t.ID}).Error
		if e == nil {
			e = db.Model(&entity.WhisperPreference{}).Where("account_id=? AND (candidate_nickname_version=? OR candidate_nickname_version IS NULL AND active_nickname_version=?)", r.OwnerID, r.ContentVersion, r.ContentVersion).Updates(map[string]any{"candidate_nickname_version": r.ContentVersion, "version": gorm.Expr("version+1")}).Error
		}
	case "whisper_capsule":
		e = db.Model(&entity.WhisperCapsule{}).Where("id=?", r.ID).Updates(map[string]any{"review_state": "scanning", "review_round": gorm.Expr("review_round+1"), "review_task_id": t.ID}).Error
	}
	if e != nil {
		return e
	}
	return loggerSink.Append(ctx, db, whisperSubmission(t, r))
}

// SetWhisperRestriction 同事务隐藏公开投影、废弃旧任务并增加限制代次，解除后只送新审。
func (s *Service) SetWhisperRestriction(ctx context.Context, token, key, rid, kind, id string, in WhisperRestrictionCommand) (out WhisperControl, err error) {
	defer s.done("whisper.content.restriction", rid, time.Now(), &err)
	if !whisperKind(kind) || !validID(id) || in.ExpectedVersion < 1 || !text(in.Reason, 500) {
		return out, command.ErrInvalid
	}
	var target command.Resource
	d := command.Definition{Kind: "whisper.content.restriction", Method: "PUT", ResourcePath: "/api/v1/admin/whispers/contents/" + kind + "/" + id + "/restriction", ActorKind: "admin", AllowedRoles: whisperGovernanceRoles, RequiredExpectations: []string{"expected_version"}, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		r, e := whisperTarget(db.WithContext(ctx), kind, id)
		if e != nil {
			return command.Discovery{}, e
		}
		target = whisperResource(r)
		return whisperPlan(ctx, db, r.task.ID)
	}
	// 期望资源在执行前解析为固定类型，不依赖回调时序。
	target = command.Resource{Kind: map[string]command.ResourceKind{"whisper_post": command.WhisperPost, "whisper_comment": command.WhisperComment, "whisper_nickname": command.WhisperNickname, "whisper_capsule": command.WhisperCapsule}[kind], ID: id}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		_, e := whisperTarget(tx.DB().WithContext(ctx), kind, id)
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		r, e := whisperTarget(db, kind, id)
		if e != nil {
			return "", e
		}
		if in.Restricted {
			restrictionID := uuid.NewString()
			reason, _, e := encryptEvidence(ctx, s.codec, "content_offline_reason", restrictionID, in.Reason)
			if e != nil {
				return "", e
			}
			x := entity.ContentRestriction{ID: restrictionID, ContentID: r.registry.ID, Kind: "violation", SourceRef: uuid.NewString(), Active: true, Version: 1, ReasonCipher: reason}
			if e = db.Create(&x).Error; e != nil {
				return "", e
			}
		} else {
			if e = db.Exec(`UPDATE public.content_restrictions x SET active=false,version=x.version+1 FROM public.content_registry c WHERE c.id=x.content_id AND c.kind=? AND c.entity_id=? AND x.kind='violation' AND x.active`, kind, r.registry.EntityID).Error; e != nil {
				return "", e
			}
		}
		if e = hideWhisper(db, r, true, tx.Now()); e != nil {
			return "", e
		}
		if e = db.Model(&entity.ModerationTask{}).Where("content_id=? AND status IN ('pending','human_required')", r.registry.ID).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		if !in.Restricted {
			if e = queueWhisper(ctx, db, r, WhisperReviewSink{s.logger}, false, tx.Now()); e != nil {
				return "", e
			}
		}
		return id, audit(db, tx.Actor().ID, r.OwnerID, "content.restrict")
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, _ string) error {
		r, e := whisperTarget(tx.DB().WithContext(ctx), kind, id)
		if e != nil {
			return e
		}
		out, e = whisperControl(tx.DB(), r)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: target, Value: in.ExpectedVersion}}})
	return
}

// NewWhisperPenalty 必须同时校验来源资源和举报版本，匿名来源作者仅在事务内解析。
func (s *Service) NewWhisperPenalty(ctx context.Context, token, key, rid, kind, id string, in WhisperPenaltyCommand) (out WhisperPenaltyResult, err error) {
	defer s.done("whisper.penalty.create", rid, time.Now(), &err)
	if (kind != "whisper_post" && kind != "whisper_comment") || !validID(id) || !validID(in.ReportID) || in.ExpectedVersion < 1 || in.ExpectedReportVersion < 1 || !text(in.Reason, 500) || map[string]string{"minor": "warning", "general": "mute", "severe": "ban"}[in.Level] == "" {
		return out, command.ErrInvalid
	}
	target := command.Resource{Kind: command.WhisperPost, ID: id}
	if kind == "whisper_comment" {
		target.Kind = command.WhisperComment
	}
	d := command.Definition{Kind: "whisper.penalty.create", Method: "POST", ResourcePath: "/api/v1/admin/whispers/contents/" + kind + "/" + id + "/penalties", ActorKind: "admin", AllowedRoles: whisperGovernanceRoles, RequiredExpectations: []string{"expected_version"}, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		r, e := whisperTarget(db, kind, id)
		if e != nil {
			return command.Discovery{}, e
		}
		p, e := whisperPlan(ctx, db, r.task.ID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.Report, ID: in.ReportID})
		if e != nil {
			return p, e
		}
		effects, e := safety.DiscoverPenaltyEffects(ctx, db, r.OwnerID)
		p.Accounts = append(p.Accounts, effects.Accounts...)
		p.Resources = append(p.Resources, effects.Resources...)
		return p, e
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		_, e := whisperTarget(tx.DB().WithContext(ctx), kind, id)
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		r, e := whisperTarget(db, kind, id)
		if e != nil {
			return "", e
		}
		var report entity.Report
		if e = db.Where("id=? AND target_kind=? AND target_id=? AND status='resolved' AND content_id=?", in.ReportID, kind, id, r.registry.ID).Take(&report).Error; e != nil {
			return "", missingWhisper(e)
		}
		if report.Version != in.ExpectedReportVersion {
			return "", command.ErrVersionConflict
		}
		action := map[string]string{"minor": "warning", "general": "mute", "severe": "ban"}[in.Level]
		var old entity.Penalty
		e = db.Where("violation_ref=? AND account_id=? AND action_kind=?", report.ID, r.OwnerID, action).Take(&old).Error
		if e == nil {
			return old.ID, nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return "", e
		}
		p := entity.Penalty{ID: uuid.NewString(), AccountID: r.OwnerID, Level: in.Level, ActionKind: action, ViolationRef: report.ID, ContentID: &r.registry.ID, ContentVersion: r.ContentVersion, StartsAt: tx.Now(), ReasonRef: uuid.NewString(), Version: 1}
		if action == "mute" {
			end := tx.Now().Add(168 * time.Hour)
			p.EndsAt = &end
		}
		p.ReasonCipher, _, e = encryptEvidence(ctx, s.codec, "penalty_reason", p.ID, in.Reason)
		if e != nil {
			return "", e
		}
		if e = db.Create(&p).Error; e != nil {
			return "", e
		}
		if e = db.Create(&entity.ContentRestriction{ID: uuid.NewString(), ContentID: r.registry.ID, Kind: "violation", SourceRef: report.ID, Active: true, Version: 1}).Error; e != nil {
			return "", e
		}
		if e = hideWhisper(db, r, true, tx.Now()); e != nil {
			return "", e
		}
		if action != "warning" {
			if e = safety.ApplyPenaltyEffects(ctx, tx, r.OwnerID); e != nil {
				return "", e
			}
		}
		if e = db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: r.OwnerID, Type: "penalty_notice", TargetRef: p.ID}).Error; e != nil {
			return "", e
		}
		return p.ID, audit(db, tx.Actor().ID, p.ID, "penalty.impose")
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var p entity.Penalty
		if e := tx.DB().WithContext(ctx).Where("id=?", ref).Take(&p).Error; e != nil {
			return e
		}
		out = WhisperPenaltyResult{ID: p.ID, Kind: p.ActionKind}
		if p.EndsAt != nil {
			v := p.EndsAt.UTC().Truncate(time.Second).Format(time.RFC3339)
			out.EndsAt = &v
		}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: target, Value: in.ExpectedVersion}}})
	return
}

func whisperSubmission(t entity.ModerationTask, r whisperSource) whisper.ReviewSubmission {
	return whisper.ReviewSubmission{TaskID: t.ID, ContentID: r.registry.ID, Kind: r.registry.Kind, ContentVersion: r.ContentVersion, Round: r.Round + 1}
}
