package moderation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperRef 不暴露昵称内部账号键，昵称ID使用随机内容注册标识。
type WhisperRef struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	ContentVersion int64  `json:"content_version"`
}

// WhisperReviewSummary 的列表不包含原文、账号或身份线索。
type WhisperReviewSummary struct {
	ID             string     `json:"id"`
	Version        int64      `json:"version"`
	Source         WhisperRef `json:"source"`
	Status         string     `json:"status"`
	CreatedAt      string     `json:"created_at"`
	AllowedActions []string   `json:"allowed_actions"`
}

// WhisperReviewDetail 仅在精确当前普通命中案件并提交审计后返回。
type WhisperReviewDetail struct {
	WhisperReviewSummary
	Body           string `json:"body"`
	SchoolRelated  bool   `json:"school_related"`
	PrivacyRecheck bool   `json:"privacy_recheck"`
}

// WhisperReviewCommand 同时绑定案件版本和正文版本，拒绝覆盖迟到决定。
type WhisperReviewCommand struct {
	ExpectedVersion        int64  `json:"expected_version"`
	ExpectedContentVersion int64  `json:"expected_content_version"`
	Decision               string `json:"decision"`
	Reason                 string `json:"reason,omitempty"`
	RevisionSuggestion     string `json:"revision_suggestion,omitempty"`
}

// WhisperReviewOutcome 区分内容决定与当前业务结果。
type WhisperReviewOutcome struct {
	ID              string `json:"id"`
	Version         int64  `json:"version"`
	ContentDecision string `json:"content_decision"`
	BusinessOutcome string `json:"business_outcome"`
}

func whisperRef(r whisperSource) WhisperRef {
	return WhisperRef{r.registry.Kind, r.ID, r.ContentVersion}
}
func whisperSummary(r whisperSource) WhisperReviewSummary {
	state := r.task.Status
	if state == "human_required" {
		state = "pending"
	}
	if state == "obsolete" {
		state = "cancelled"
	}
	return WhisperReviewSummary{r.task.ID, r.task.Version, whisperRef(r), state, r.task.CreatedAt.UTC().Truncate(time.Second).Format(time.RFC3339), []string{}}
}
func (s *Service) whisperCursorActor(actor string) string {
	m := hmac.New(sha256.New, s.cfg.CursorKey)
	m.Write([]byte("whisper.cursor.actor:" + actor))
	return hex.EncodeToString(m.Sum(nil))
}
func (s *Service) whisperAfter(actor, purpose, cursor string) (string, error) {
	return s.after(s.whisperCursorActor(actor), "whisper."+purpose, cursor)
}
func (s *Service) whisperCursor(actor, purpose, id string) string {
	return s.cursor(s.whisperCursorActor(actor), "whisper."+purpose, id)
}

// WhisperReviews 列出树洞普通命中元数据，旧业务及危机来源均不可混入。
func (s *Service) WhisperReviews(ctx context.Context, token, rid, cursor string, limit int) (out whisper.Page[WhisperReviewSummary], err error) {
	defer s.done("whisper.review.list", rid, time.Now(), &err)
	if limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	out.Items = []WhisperReviewSummary{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorize(ctx, db, token, command.Discovery{})
		if e != nil {
			return e
		}
		after, e := s.whisperAfter(actor, "reviews", cursor)
		if e != nil {
			return e
		}
		var ids []string
		q := db.Table("public.moderation_tasks AS t").Select("t.id").Joins("JOIN public.content_registry c ON c.id=t.content_id").Joins("JOIN public.moderation_scans s ON s.task_id=t.id").Where("c.kind IN ? AND NOT s.crisis_hit AND (s.school_related OR s.ordinary_hit) AND t.status IN ('human_required','approved','rejected') AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry c2 ON c2.id=x.content_id WHERE c2.kind=c.kind AND c2.entity_id=c.entity_id AND x.active AND x.kind='crisis')", []string{"whisper_post", "whisper_comment", "whisper_nickname", "whisper_capsule"})
		if after != "" {
			q = q.Where("t.id>?", after)
		}
		if e = q.Order("t.id").Limit(limit + 1).Scan(&ids).Error; e != nil {
			return e
		}
		if len(ids) > limit {
			ids = ids[:limit]
			v := s.whisperCursor(actor, "reviews", ids[len(ids)-1])
			out.NextCursor = &v
		}
		for _, id := range ids {
			r, e := loadWhisper(db, id)
			if e != nil {
				return e
			}
			if whisperCurrent(r) {
				out.Items = append(out.Items, whisperSummary(r))
			}
		}
		return nil
	})
	if err != nil {
		out = whisper.Page[WhisperReviewSummary]{}
	}
	return
}
func ordinaryWhisper(db *gorm.DB, r whisperSource) (scan entity.ModerationScan, err error) {
	if !whisperCurrent(r) {
		return scan, command.ErrNotFound
	}
	err = db.Where("task_id=? AND NOT crisis_hit AND (school_related OR ordinary_hit)", r.task.ID).Take(&scan).Error
	if err != nil {
		return scan, missingWhisper(err)
	}
	var n int64
	err = db.Raw(`SELECT count(*) FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE c.kind=? AND c.entity_id=? AND x.kind='crisis' AND x.active`, r.registry.Kind, r.registry.EntityID).Scan(&n).Error
	if err == nil && n > 0 {
		err = command.ErrNotFound
	}
	return
}
func (s *Service) whisperPayload(ctx context.Context, db *gorm.DB, token string, r whisperSource) (string, error) {
	hash := sha256.Sum256([]byte(token))
	var raw []struct{ Body json.RawMessage }
	if e := db.Raw("SELECT body FROM public.read_whisper_review_payload(decode(?,'hex'),?)", hex.EncodeToString(hash[:]), r.task.ID).Scan(&raw).Error; e != nil {
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
	e := whisperPlain(ctx, s.codec, purpose, r.ID, r.ContentVersion, raw[0].Body, &body)
	return body, e
}

// WhisperReview 验证当前角色、任务与屏障，在审计事务提交后才允许 handler 发送正文。
func (s *Service) WhisperReview(ctx context.Context, token, rid, id string) (out WhisperReviewDetail, err error) {
	defer s.done("whisper.review.detail", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		p, e := whisperPlan(ctx, db, id)
		if e != nil {
			return e
		}
		actor, e := s.authorize(ctx, db, token, p)
		if e != nil {
			return e
		}
		r, e := loadWhisper(db, id)
		if e != nil {
			return e
		}
		scan, e := ordinaryWhisper(db, r)
		if e != nil {
			return e
		}
		body, e := s.whisperPayload(ctx, db, token, r)
		if e != nil {
			return e
		}
		out = WhisperReviewDetail{whisperSummary(r), body, scan.SchoolRelated, r.task.PrivacyRecheck}
		if r.task.Status == "human_required" {
			out.AllowedActions = []string{"whisperAdminReviewDecide"}
			var n int64
			if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=? AND role='super_admin'", actor).Count(&n).Error; e != nil {
				return e
			}
			if n > 0 {
				out.AllowedActions = append(out.AllowedActions, "whisperAdminReviewEscalate")
			}
		}
		return audit(db, actor, r.OwnerID, "moderation.view")
	})
	if err != nil {
		out = WhisperReviewDetail{}
	}
	return
}

// DecideWhisper 将人工决定和自动审核使用同一资格栅栏，并保留生命周期幂等回执。
func (s *Service) DecideWhisper(ctx context.Context, token, key, rid, id string, in WhisperReviewCommand) (out WhisperReviewOutcome, err error) {
	defer s.done("whisper.review.decide", rid, time.Now(), &err)
	if !validID(id) || in.ExpectedVersion < 1 || in.ExpectedContentVersion < 1 || (in.Decision != "approved" && in.Decision != "rejected") || in.Decision == "rejected" && (!text(in.Reason, 500) || !text(in.RevisionSuggestion, 500)) || len(in.Reason) > 2000 || len(in.RevisionSuggestion) > 2000 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "whisper.review.decide", Method: "POST", ResourcePath: "/api/v1/admin/whispers/reviews/" + id + "/decision", ActorKind: "admin", AllowedRoles: []string{"review_operator", "super_admin"}, RequiredExpectations: []string{"expected_version"}, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return whisperPlan(ctx, db, id)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		r, e := loadWhisper(tx.DB().WithContext(ctx), id)
		if e != nil {
			return e
		}
		_, e = ordinaryWhisper(tx.DB(), r)
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		r, e := loadWhisper(db, id)
		if e != nil {
			return "", e
		}
		scan, e := ordinaryWhisper(db, r)
		if e != nil {
			return "", e
		}
		if r.ContentVersion != in.ExpectedContentVersion {
			return "", command.ErrVersionConflict
		}
		if r.task.Status != "human_required" {
			return "", command.ErrConflict
		}
		ok, e := whisperEligible(db, r)
		if e != nil {
			return "", e
		}
		status := in.Decision
		if ok {
			var a WhisperAssessment
			if e = decryptEvidence(ctx, s.codec, "whisper_moderation_projection", id, scan.ProjectionCipher, &a); e != nil {
				return "", e
			}
			if e = applyWhisperReview(ctx, db, s.codec, r, a, status, tx.Now()); e != nil {
				return "", e
			}
		} else {
			status = "cancelled"
		}
		u := map[string]any{"status": status, "decision": in.Decision, "reviewer_id": tx.Actor().ID, "version": gorm.Expr("version+1"), "reason": nil, "revision_suggestion": nil}
		if in.Decision == "rejected" {
			u["reason"] = in.Reason
			u["revision_suggestion"] = in.RevisionSuggestion
		}
		if e = db.Model(&entity.ModerationTask{}).Where("id=?", id).Updates(u).Error; e != nil {
			return "", e
		}
		return id, audit(db, tx.Actor().ID, r.OwnerID, "moderation.decision")
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		r, e := loadWhisper(tx.DB().WithContext(ctx), ref)
		if e != nil {
			return e
		}
		if r.task.Decision == nil {
			return command.ErrConflict
		}
		out = WhisperReviewOutcome{ref, r.task.Version, *r.task.Decision, "cancelled"}
		if r.task.Status == "rejected" {
			out.BusinessOutcome = "rejected"
		} else if r.task.Status == "approved" {
			ok, e := whisperEligible(tx.DB(), r)
			if e != nil {
				return e
			}
			if ok {
				out.BusinessOutcome = map[string]string{"whisper_post": "published", "whisper_comment": "published", "whisper_nickname": "nickname_activated", "whisper_capsule": "capsule_locked"}[r.registry.Kind]
				if r.registry.Kind == "whisper_nickname" {
					out.BusinessOutcome = "nickname_activated"
				}
				if r.registry.Kind == "whisper_capsule" && !r.UnlockAt.After(tx.Now()) {
					out.BusinessOutcome = "capsule_unlocked"
				}
			}
		}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.ModerationTask, ID: id}, Value: in.ExpectedVersion}}})
	return
}
