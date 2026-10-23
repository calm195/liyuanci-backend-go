package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Summary 只包含普通命中案件的技术元数据，无正文或作者身份。
type Summary struct {
	ID             string `json:"id"`
	Version        int64  `json:"version"`
	Status         string `json:"status"`
	Kind           string `json:"kind"`
	ContentVersion int64  `json:"content_version"`
}

// Page 是经过服务端案件授权过滤的稳定游标队列。
type Page struct {
	Items      []Summary `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

// Case 返回精确案件原版本正文，仅在访问审计提交后交付。
type Case struct {
	ID             string `json:"id"`
	Version        int64  `json:"version"`
	ContentVersion int64  `json:"content_version"`
	Body           string `json:"body"`
	SchoolRelated  bool   `json:"school_related"`
	Status         string `json:"status"`
}

// Decision 使用独立案件版本，驳回原因和修改建议分别必填。
type Decision struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason,omitempty"`
	RevisionSuggestion string `json:"revision_suggestion,omitempty"`
	ExpectedVersion    int64  `json:"expected_version"`
}

// Outcome 区分内容判定与业务执行结果；心愿批准只到待上架，申请批准仍须复核当前交互资格。
type Outcome struct {
	ContentDecision string `json:"content_decision"`
	BusinessOutcome string `json:"business_outcome"`
	Version         int64  `json:"version"`
}

func (s *Service) preflight(ctx context.Context, token string) error {
	return s.transaction(ctx, func(db *gorm.DB) error {
		_, e := s.authorize(db.Statement.Context, db, token, command.Discovery{})
		return e
	})
}
func ordinaryScan(db *gorm.DB, id string) (out entity.ModerationScan, err error) {
	err = db.Where("task_id=? AND NOT crisis_hit AND (school_related OR ordinary_hit)", id).Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	if err == nil {
		var c entity.ContentRegistry
		var t entity.ModerationTask
		if e := db.Where("id=?", id).Take(&t).Error; e != nil {
			return out, e
		}
		if e := db.Where("id=?", t.ContentID).Take(&c).Error; e != nil {
			return out, e
		}
		var isolated int64
		if e := db.Model(&entity.ContentRestriction{}).Where("content_id=? AND kind='crisis' AND active", c.ID).Count(&isolated).Error; e != nil {
			return out, e
		}
		if isolated > 0 {
			return out, command.ErrNotFound
		}
		if c.Kind == "message" && !out.OrdinaryHit {
			return out, command.ErrNotFound
		}
	}
	return
}

// Cases 列表只开放心愿、应愿留言、私聊及故事的普通命中案件，危机正文不因普通审核权限而开放。
func (s *Service) Cases(ctx context.Context, token, rid, cursor string, limit int) (out Page, err error) {
	defer func() {
		if err != nil {
			out = Page{}
		}
	}()
	defer s.done("cases.list", rid, time.Now(), &err)
	if limit < 1 || limit > 100 {
		return out, command.ErrInvalid
	}
	out.Items = []Summary{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorize(db.Statement.Context, db, token, command.Discovery{})
		if e != nil {
			return e
		}
		after, e := s.after(actor, "cases", cursor)
		if e != nil {
			return e
		}
		q := db.Model(&entity.ModerationTask{}).Table("public.moderation_tasks AS t").Select("t.id,t.version,t.status,c.kind,t.content_version").Joins("JOIN public.moderation_scans s ON s.task_id=t.id").Joins("JOIN public.content_registry c ON c.id=t.content_id AND c.kind IN ('wish','application','message','story')").Joins("JOIN public.accounts a ON a.id=c.author_id AND a.lifecycle='active'").Where("NOT EXISTS(SELECT 1 FROM public.content_restrictions r WHERE r.content_id=c.id AND r.kind='crisis' AND r.active) AND NOT s.crisis_hit AND (s.ordinary_hit OR (c.kind<>'message' AND s.school_related)) AND t.status IN ?", []string{"human_required", "approved", "rejected"})
		if after != "" {
			q = q.Where("t.id>?", after)
		}
		var rows []Summary
		if e = q.Order("t.id").Limit(limit + 1).Scan(&rows).Error; e != nil {
			return e
		}
		if len(rows) > limit {
			rows = rows[:limit]
			next := s.cursor(actor, "cases", rows[len(rows)-1].ID)
			out.NextCursor = &next
		}
		out.Items = rows
		if out.Items == nil {
			out.Items = []Summary{}
		}
		return nil
	})
	return
}

// Case 只返回案件精确原版本；旧版本、已删除内容和危机案件返回404且不解密。
func (s *Service) Case(ctx context.Context, token, rid, id string) (out Case, err error) {
	defer func() {
		if err != nil {
			out = Case{}
		}
	}()
	defer s.done("cases.detail", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	if e := s.preflight(ctx, token); e != nil {
		return out, e
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		ctx := db.Statement.Context
		plan, e := discover(ctx, db, id)
		if e != nil {
			return e
		}
		actor, e := s.authorize(ctx, db, token, plan)
		if e != nil {
			return e
		}
		row, e := load(db, id)
		if e != nil {
			return e
		}
		scan, e := ordinaryScan(db, id)
		if e != nil {
			return e
		}
		if (row.message != nil && (!row.conversation.CanRead(row.message.SenderID) || row.message.Status == "cancelled")) || row.account.Lifecycle != "active" || row.wish.DraftDeletedAt != nil || (row.application != nil && (row.application.Status == "withdrawn" || row.application.Status == "invalidated")) || !currentBinding(row) || row.task.Status == "obsolete" || row.task.Status == "cancelled" {
			return command.ErrNotFound
		}
		if row.story != nil {
			ok, e := eligible(db, row)
			if e != nil {
				return e
			}
			if !ok {
				return command.ErrNotFound
			}
		}
		var held int64
		if row.story == nil {
			if e = db.Model(&entity.PrivacyHold{}).Where("target_kind='wish' AND target_id=? AND (status='active' OR hold_epoch>?)", row.wish.ID, row.task.HoldEpoch).Count(&held).Error; e != nil {
				return e
			}
		}
		if held > 0 || row.task.SubjectEpoch != row.account.AuthEpoch {
			return command.ErrNotFound
		}
		body, e := decryptContent(ctx, s.codec, row)
		if e != nil {
			return e
		}
		if e = audit(db, actor, row.account.ID, "moderation.detail"); e != nil {
			return e
		}
		out = Case{ID: id, Version: row.task.Version, ContentVersion: row.task.ContentVersion, Body: body.Body, SchoolRelated: scan.SchoolRelated, Status: row.task.Status}
		return nil
	})
	return
}
func validateDecision(in Decision) error {
	if in.ExpectedVersion < 1 || (in.Decision != "approved" && in.Decision != "rejected") || !utf8.ValidString(in.Reason) || !utf8.ValidString(in.RevisionSuggestion) || len(in.Reason) > 2000 || len(in.RevisionSuggestion) > 2000 || in.Decision == "rejected" && (strings.TrimSpace(in.Reason) == "" || strings.TrimSpace(in.RevisionSuggestion) == "") {
		return command.ErrInvalid
	}
	return nil
}

// Decide 在当前案件/心愿/账号锁下复核资格、版本和屏障，审计与决定同事务提交。
func (s *Service) Decide(ctx context.Context, token, key, rid, id string, in Decision) (out Outcome, err error) {
	defer func() {
		if err != nil {
			out = Outcome{}
		}
	}()
	defer s.done("cases.decision", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	if e := validateDecision(in); e != nil {
		return out, e
	}
	if e := s.preflight(ctx, token); e != nil {
		return out, e
	}
	d := command.Definition{Kind: "moderation.decision", Method: "POST", ResourcePath: "/api/v1/admin/moderation-tasks/" + id + "/decision", ActorKind: "admin", AllowedRoles: []string{"review_operator", "super_admin"}, RequiredExpectations: []string{"expected_version"}, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return discover(ctx, db, id)
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		row, e := load(tx.DB().WithContext(ctx), id)
		if e != nil {
			return e
		}
		if _, e = ordinaryScan(tx.DB().WithContext(ctx), id); e != nil {
			return e
		}
		if (row.message != nil && (!row.conversation.CanRead(row.message.SenderID) || row.message.Status == "cancelled")) || row.account.Lifecycle != "active" || row.wish.DraftDeletedAt != nil {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		row, e := load(db, id)
		if e != nil {
			return "", e
		}
		scan, e := ordinaryScan(db, id)
		if e != nil {
			return "", e
		}
		if row.task.Status != "human_required" {
			return "", command.ErrConflict
		}
		if !currentBinding(row) {
			return "", command.ErrVersionConflict
		}
		// 屏障代次变化不能被旧案件放行；故事只释放新稿已覆盖的本域屏障，父心愿屏障独立。
		var epoch int64
		if row.story != nil {
			holds, e := wish.ReadStoryHolds(ctx, db, row.wish.ID)
			if e != nil {
				return "", e
			}
			epoch = holds.Epoch
		} else {
			if e = db.Model(&entity.PrivacyHold{}).Select("coalesce(max(hold_epoch),0)").Where("target_kind='wish' AND target_id=?", row.wish.ID).Scan(&epoch).Error; e != nil {
				return "", e
			}
		}
		if epoch != row.task.HoldEpoch {
			return "", command.ErrVersionConflict
		}
		ok, e := eligible(db, row)
		if e != nil {
			return "", e
		}
		status := in.Decision
		business := "pending_review"
		if !ok {
			status = "cancelled"
			business = "cancelled"
			if row.story != nil {
				if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", row.wish.ID, row.task.ContentVersion).Updates(map[string]any{"moderation_status": "cancelled", "public_body": nil}).Error; e != nil {
					return "", e
				}
			}
			if row.message != nil {
				if e = chat.CancelMessage(ctx, db, *row.message); e != nil {
					return "", e
				}
			}
			if row.application != nil {
				if e = wish.CancelApplication(ctx, db, *row.application, "invalidated", "eligibility_changed"); e != nil {
					return "", e
				}
			}
		} else {
			var envelope securedata.Envelope
			if json.Unmarshal(scan.ProjectionCipher, &envelope) != nil {
				return "", command.ErrUnavailable
			}
			plain, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "moderation_projection", ObjectID: id, Version: 1}, envelope)
			defer clear(plain)
			if e != nil {
				return "", command.ErrUnavailable
			}
			var candidate struct {
				Body string
				Tags wish.IdentityTags
			}
			if json.Unmarshal(plain, &candidate) != nil || !candidateBody(row, candidate.Body) {
				return "", command.ErrUnavailable
			}
			var body *string
			tags := []byte(`{}`)
			if in.Decision == "approved" {
				body = &candidate.Body
				tags, _ = json.Marshal(candidate.Tags)
				business = "ready"
				if row.task.CrisisRecheck && row.application == nil && row.message == nil && row.story == nil && row.wish.State != "pending_review" {
					business = row.wish.State
				}
				if row.story != nil {
					business = "published"
				}
				if row.message != nil {
					business = "delivered"
				}
				if row.application != nil {
					business = "active"
					if row.task.CrisisRecheck && row.application.Status == "accepted" {
						business = "accepted"
					}
				}
			}
			if e = setReview(db, s.codec, row, in.Decision, body, tags); e != nil {
				return "", e
			}
		}
		actor := tx.Actor().ID
		updates := map[string]any{"status": status, "decision": in.Decision, "version": gorm.Expr("version+1"), "reviewer_id": actor, "reason": nil, "revision_suggestion": nil}
		if in.Decision == "rejected" {
			updates["reason"] = in.Reason
			updates["revision_suggestion"] = in.RevisionSuggestion
		}
		if e = db.Model(&entity.ModerationTask{}).Where("id=?", id).Updates(updates).Error; e != nil {
			return "", e
		}
		if e = audit(db, actor, row.account.ID, "moderation.decision"); e != nil {
			return "", e
		}
		out = Outcome{in.Decision, business, row.task.Version + 1}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		row, e := load(tx.DB().WithContext(ctx), ref)
		if e != nil {
			return e
		}
		if row.task.Decision == nil {
			return command.ErrConflict
		}
		business := "pending_review"
		if row.task.Status == "cancelled" || row.task.Status == "obsolete" {
			business = "cancelled"
		} else if *row.task.Decision == "approved" {
			business = "ready"
			if row.task.CrisisRecheck && row.application == nil && row.message == nil && row.story == nil {
				business = row.wish.State
			}
			if row.story != nil {
				ok, e := eligible(tx.DB().WithContext(ctx), row)
				if e != nil {
					return e
				}
				business = "cancelled"
				if ok {
					business = "published"
				}
			}
			if row.message != nil {
				business = row.message.Status
				if business != "delivered" {
					business = "cancelled"
				}
			}
			if row.application != nil {
				business = "active"
				if row.task.CrisisRecheck && row.application.Status == "accepted" {
					business = "accepted"
				}
			}
		}
		out = Outcome{*row.task.Decision, business, row.task.Version}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(in), Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.ModerationTask, ID: id}, Value: in.ExpectedVersion}}})
	return
}
