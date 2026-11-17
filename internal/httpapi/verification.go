package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/verification"
)

// VerificationStudents 仅暴露本人认证申请及状态。
type VerificationStudents interface {
	Submit(context.Context, string, string, string, verification.Input) (verification.Result, error)
	Current(context.Context, string, string) (verification.Result, error)
}

// VerificationReviews 由领域服务复核当前后台会话与审核岗位，不授予学生入口审核权限。
type VerificationReviews interface {
	ListWorkspace(context.Context, string, string, string, int, string) (verification.Page, error)
	ListWorkspaceWithCreatedAt(context.Context, string, string, string, int, string) (verification.Page, error)
	Detail(context.Context, string, string, string) (verification.Case, error)
	History(context.Context, string, string, string) (verification.HistoryCase, error)
	Card(context.Context, string, string, string) ([]byte, error)
	Decide(context.Context, string, string, string, string, verification.Decision) (verification.Result, error)
}

// RegisterVerification 显式注册人工认证路由，默认健康服务不启用供应商合成适配器。
func RegisterVerification(r *gin.Engine, students VerificationStudents, reviews VerificationReviews) error {
	if r == nil || students == nil || reviews == nil {
		return command.ErrInvalid
	}
	group := r.Group("/api/v1")
	group.Use(func(c *gin.Context) {

		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.Next()
	})
	group.POST("/me/verification-requests", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var wire struct {
			StudentNumber   string          `json:"student_number"`
			Name            string          `json:"name"`
			AssetID         string          `json:"asset_id"`
			ExpectedVersion json.RawMessage `json:"expected_version"`
		}
		if strictJSON(c, &wire) != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		in := verification.Input{StudentNumber: wire.StudentNumber, Name: wire.Name, AssetID: wire.AssetID}
		// 可选整数的显式null不能被当成首次申请省略，保持与共享JSON Schema一致。
		if len(wire.ExpectedVersion) > 0 {
			var version int64
			if bytes.Equal(bytes.TrimSpace(wire.ExpectedVersion), []byte("null")) || json.Unmarshal(wire.ExpectedVersion, &version) != nil ||
				version < 1 {
				adminError(c, command.ErrInvalid)
				return
			}
			in.ExpectedVersion = &version
		}
		out, e := students.Submit(c.Request.Context(), token, key, c.GetString("request_id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	group.GET("/me/verification-request", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := students.Current(c.Request.Context(), token, c.GetString("request_id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.GET("/admin/identity-reviews", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		query, e := parseIdentityReviewQuery(c.Request.URL.RawQuery)
		if e != nil {
			adminError(c, e)
			return
		}
		list := reviews.ListWorkspace
		if query.includeCreatedAt {
			list = reviews.ListWorkspaceWithCreatedAt
		}
		out, e := list(c.Request.Context(), token, c.GetString("request_id"), query.cursor, query.limit, query.view)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.GET("/admin/identity-reviews/:id/history", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		if c.Request.URL.RawQuery != "" {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := reviews.History(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.GET("/admin/identity-reviews/:id", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := reviews.Detail(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.GET("/admin/identity-reviews/:id/student-card", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := reviews.Card(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		defer clear(out)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "image/png", out)
	})
	group.POST("/admin/identity-reviews/:id/decision", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in verification.Decision
		if strictJSON(c, &in) != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := reviews.Decide(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}

type identityReviewQuery struct {
	cursor           string
	limit            int
	view             string
	includeCreatedAt bool
}

func parseIdentityReviewQuery(raw string) (out identityReviewQuery, err error) {
	out.limit = 20
	query, err := url.ParseQuery(raw)
	if err != nil {
		return out, command.ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 {
			return out, command.ErrInvalid
		}
		switch key {
		case "cursor":
			out.cursor = values[0]
		case "limit":
			out.limit, err = strconv.Atoi(values[0])
		case "view":
			out.view = values[0]
		case "include":
			if values[0] != "created_at" {
				return out, command.ErrInvalid
			}
			out.includeCreatedAt = true
		default:
			return out, command.ErrInvalid
		}
		if err != nil {
			return out, command.ErrInvalid
		}
	}
	if out.limit < 1 || out.limit > 50 || len(out.cursor) > 256 ||
		out.view != "" && out.view != "pending" && out.view != "history" ||
		out.includeCreatedAt && out.view == "" {
		return out, command.ErrInvalid
	}
	return out, nil
}
