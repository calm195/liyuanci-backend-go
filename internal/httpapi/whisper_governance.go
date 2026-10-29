package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperEvidenceDeletionAPI 使用独立证据删除池并隔离旧后台回执。
type WhisperEvidenceDeletionAPI interface {
	WhisperEvidenceStatus(context.Context, string, string, string) (privacy.Receipt, error)
	DeleteWhisperEvidence(context.Context, string, string, string, string, privacy.EvidenceInput) (privacy.EvidenceAccepted, error)
}

// RegisterWhisperGovernance 显式装配独立树洞后台和举报投影；生产总装配仍须通过隐私验收。
// 证据删除复用独立删除身份的持久框架，受理成功仅表示进入清理队列。
func RegisterWhisperGovernance(r *gin.Engine, s *moderation.Service, crisis *moderation.Service, student *moderation.Service, evidence WhisperEvidenceDeletionAPI) error {
	if r == nil || s == nil || crisis == nil || student == nil || evidence == nil {
		return command.ErrInvalid
	}
	handle := func(op string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			c.Header("Pragma", "no-cache")
			ctx, rid := c.Request.Context(), c.GetString("request_id")
			token, e := whisperBearer(c, true)
			if e != nil {
				whisperError(c, e)
				return
			}
			list := op == "reviews" || op == "reports" || op == "crises"
			if !list && len(c.Request.URL.Query()) > 0 {
				whisperError(c, command.ErrInvalid)
				return
			}
			id := c.Param("id")
			if id != "" && !whisper.ValidID(id) {
				whisperError(c, command.ErrInvalid)
				return
			}
			kind := c.Param("kind")
			if kind != "" && !slicesWhisper(kind, "whisper_post", "whisper_comment", "whisper_nickname", "whisper_capsule") {
				whisperError(c, command.ErrNotFound)
				return
			}
			var out any
			status := http.StatusOK
			if c.Request.Method == http.MethodGet {
				switch op {
				case "reviews", "reports", "crises":
					q, failure := whisperQuery(c)
					if failure != nil {
						e = failure
						break
					}
					switch op {
					case "reviews":
						out, e = s.WhisperReviews(ctx, token, rid, q.Cursor, q.Limit)
					case "reports":
						out, e = s.WhisperReports(ctx, token, rid, q.Cursor, q.Limit)
					case "crises":
						out, e = crisis.WhisperCrises(ctx, token, rid, q.Cursor, q.Limit)
					}
				case "review":
					out, e = s.WhisperReview(ctx, token, rid, id)
				case "report":
					out, e = s.WhisperReport(ctx, token, rid, id)
				case "content":
					out, e = s.WhisperContent(ctx, token, rid, kind, id)
				case "crisis":
					out, e = crisis.WhisperCrisis(ctx, token, rid, id)
				case "evidence":
					out, e = crisis.WhisperEvidence(ctx, token, rid, id)
				case "deletion":
					var v privacy.Receipt
					v, e = evidence.WhisperEvidenceStatus(ctx, token, rid, id)
					out = whisperDeletion(id, v.Status)
				}
			} else {
				key := c.GetHeader("Idempotency-Key")
				if !whisper.ValidKey(key) || len(c.Request.Header.Values("Idempotency-Key")) != 1 {
					whisperError(c, command.ErrInvalid)
					return
				}
				raw, failure := readWhisperJSON(c)
				if failure != nil {
					whisperError(c, failure)
					return
				}
				decode := func(target any, required []string, allowed ...string) error {
					if !strictWhisperUnicode(raw) {
						return command.ErrInvalid
					}
					fields, e := wishObject(raw, allowed)
					if e != nil {
						return e
					}
					for _, name := range required {
						if _, ok := fields[name]; !ok {
							return command.ErrInvalid
						}
					}
					return json.Unmarshal(raw, target)
				}
				switch op {
				case "decide":
					var v moderation.WhisperReviewCommand
					e = decode(&v, []string{"expected_version", "expected_content_version", "decision"}, "expected_version", "expected_content_version", "decision", "reason", "revision_suggestion")
					if e == nil {
						out, e = s.DecideWhisper(ctx, token, key, rid, id, v)
					}
				case "restrict":
					var v moderation.WhisperRestrictionCommand
					e = decode(&v, []string{"expected_version", "restricted", "reason"}, "expected_version", "restricted", "reason")
					if e == nil {
						out, e = s.SetWhisperRestriction(ctx, token, key, rid, kind, id, v)
					}
				case "penalty":
					var v moderation.WhisperPenaltyCommand
					e = decode(&v, []string{"expected_version", "report_id", "expected_report_version", "level", "reason"}, "expected_version", "report_id", "expected_report_version", "level", "reason")
					if e == nil {
						out, e = s.NewWhisperPenalty(ctx, token, key, rid, kind, id, v)
					}
				case "report_create":
					var v moderation.WhisperReportInput
					e = decode(&v, []string{"source", "type_code"}, "source", "type_code", "description")
					if e == nil {
						var fields map[string]json.RawMessage
						_ = json.Unmarshal(raw, &fields)
						_, e = whisperObject(fields["source"], []string{"kind", "id", "content_version"})
					}
					if e == nil {
						out, e = student.CreateWhisperReport(ctx, token, key, rid, v)
						status = http.StatusAccepted
					}
				case "report_decide":
					var v moderation.WhisperReportDecision
					e = decode(&v, []string{"expected_version", "decision", "reason"}, "expected_version", "decision", "reason")
					if e == nil {
						out, e = s.DecideWhisperReport(ctx, token, key, rid, id, v)
					}
				case "review_escalate", "report_escalate", "false_positive", "act":
					var v moderation.CrisisCommand
					allowed := []string{"expected_version", "reason"}
					required := append([]string{}, allowed...)
					if op == "act" {
						allowed = append(allowed, "action")
						required = append(required, "action")
					}
					e = decode(&v, required, allowed...)
					if e == nil {
						if strings.HasSuffix(op, "escalate") {
							source := "review"
							if op == "report_escalate" {
								source = "report"
							}
							out, e = crisis.EscalateWhisperCrisis(ctx, token, key, rid, source, id, v)
						} else {
							out, e = crisis.ActOnWhisperCrisis(ctx, token, key, rid, id, v, op == "false_positive")
						}
					}
				case "evidence_delete":
					var v struct {
						ExpectedVersion int64  `json:"expected_version"`
						Confirmation    string `json:"confirmation"`
						Reason          string `json:"reason"`
					}
					e = decode(&v, []string{"expected_version", "confirmation", "reason"}, "expected_version", "confirmation", "reason")
					if e == nil && (v.Confirmation != "DELETE_EVIDENCE_SCOPE" || v.ExpectedVersion < 1 || strings.TrimSpace(v.Reason) == "" || len([]rune(v.Reason)) > 500) {
						e = command.ErrInvalid
					}
					if e == nil {
						_, e = crisis.WhisperEvidence(ctx, token, rid, id)
					}
					if e == nil {
						var accepted privacy.EvidenceAccepted
						accepted, e = evidence.DeleteWhisperEvidence(ctx, token, key, rid, id, privacy.EvidenceInput{Reason: v.Reason, ExpectedVersion: v.ExpectedVersion})
						out = whisperDeletion(accepted.ID, "processing")
						status = http.StatusAccepted
					}
				}
			}
			if e != nil {
				whisperError(c, e)
				return
			}
			adminResult(c, status, out)
		}
	}
	r.POST("/api/v1/whispers/reports", handle("report_create"))
	base := "/api/v1/admin/whispers"
	r.GET(base+"/reviews", handle("reviews"))
	r.GET(base+"/reviews/:id", handle("review"))
	r.POST(base+"/reviews/:id/decision", handle("decide"))
	r.POST(base+"/reviews/:id/escalate", handle("review_escalate"))
	r.GET(base+"/contents/:kind/:id", handle("content"))
	r.PUT(base+"/contents/:kind/:id/restriction", handle("restrict"))
	r.POST(base+"/contents/:kind/:id/penalties", handle("penalty"))
	r.GET(base+"/reports", handle("reports"))
	r.GET(base+"/reports/:id", handle("report"))
	r.POST(base+"/reports/:id/decision", handle("report_decide"))
	r.POST(base+"/reports/:id/escalate", handle("report_escalate"))
	r.GET(base+"/crises", handle("crises"))
	r.GET(base+"/crises/:id", handle("crisis"))
	r.POST(base+"/crises/:id/actions", handle("act"))
	r.POST(base+"/crises/:id/false-positive", handle("false_positive"))
	r.GET(base+"/evidence/:id", handle("evidence"))
	r.POST(base+"/evidence/:id/deletion-requests", handle("evidence_delete"))
	r.GET(base+"/deletions/:id", handle("deletion"))
	return nil
}
func whisperDeletion(id, status string) any {
	return struct {
		ID             string   `json:"id"`
		Status         string   `json:"status"`
		Scope          string   `json:"scope"`
		AllowedActions []string `json:"allowed_actions"`
	}{id, status, "evidence", []string{}}
}
