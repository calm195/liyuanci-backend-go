package httpapi

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
)

// PrivacyAPI 是本人注销及匿名回执的最小边界，不提供账号参数或普通数据导出。
type PrivacyAPI interface {
	Request(context.Context, string, string, string, privacy.DeletionInput) (privacy.Accepted, error)
	Status(context.Context, string, string) (privacy.Receipt, error)
}

// EvidenceDeletionAPI 只能使用独立证据删除池并在服务端复核超管。
type EvidenceDeletionAPI interface {
	EvidenceStatus(context.Context, string, string, string) (privacy.Receipt, error)
	DeleteEvidence(context.Context, string, string, string, string, privacy.EvidenceInput) (privacy.EvidenceAccepted, error)
}

// RegisterPrivacy 显式注册注销入口及无身份回执；所有结果禁止缓存。
func RegisterPrivacy(r *gin.Engine, s PrivacyAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	r.POST("/api/v1/me/deletion-requests", func(c *gin.Context) {
		privacyHeaders(c)
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in privacy.DeletionInput
		if !interactionJSON(c, []string{"confirmation", "expected_version"}, &in) || in.Confirmation != "delete_account" || in.ExpectedVersion < 1 {
			returnInvalid(c)
			return
		}
		out, e := s.Request(c.Request.Context(), token, key, c.GetString("request_id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	r.GET("/api/v1/deletion-receipts/:receipt", func(c *gin.Context) {
		privacyHeaders(c)
		if len(c.Request.URL.Query()) != 0 {
			returnInvalid(c)
			return
		}
		out, e := s.Status(c.Request.Context(), c.GetString("request_id"), c.Param("receipt"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}

// RegisterEvidenceDeletion 注册超管永久删除的受理入口，202不表示在线对象已清理。
func RegisterEvidenceDeletion(r *gin.Engine, s EvidenceDeletionAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	r.GET("/api/v1/admin/evidence-deletion-jobs/:id", func(c *gin.Context) {
		privacyHeaders(c)
		token, ok := bearer(c)
		if !ok {
			return
		}
		if len(c.Request.URL.Query()) != 0 {
			returnInvalid(c)
			return
		}
		out, e := s.EvidenceStatus(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	r.DELETE("/api/v1/admin/evidence/:id", func(c *gin.Context) {
		privacyHeaders(c)
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in privacy.EvidenceInput
		if !interactionJSON(c, []string{"reason", "expected_version"}, &in) || in.ExpectedVersion < 1 || !utf8.ValidString(in.Reason) || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2000 {
			returnInvalid(c)
			return
		}
		out, e := s.DeleteEvidence(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	return nil
}
func privacyHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Referrer-Policy", "no-referrer")
}
