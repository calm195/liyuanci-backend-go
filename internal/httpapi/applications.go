package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// ApplicationsAPI 只包含应愿、同意与普通回退，不接收客户端指定的关系或审核状态。
type ApplicationsAPI interface {
	Apply(context.Context, string, string, string, string, wish.ApplicationInput) (wish.ApplicationResult, error)
	WithdrawApplication(context.Context, string, string, string, string, int64) (wish.ApplicationResult, error)
	AcceptApplication(context.Context, string, string, string, string, wish.AcceptInput) (wish.MatchResult, error)
	Rollback(context.Context, string, string, string, string, wish.RollbackInput) (wish.CreatedDraft, error)
	Applications(context.Context, string, string, string, wish.ApplicationQuery) (wish.ApplicationPage, error)
	OwnApplications(context.Context, string, string, wish.ApplicationQuery) (wish.ApplicationPage, error)
}

// RegisterApplications 显式注册正式六项接口，必须由应用装配方与心愿模块一起启用。
func RegisterApplications(r *gin.Engine, service ApplicationsAPI) error {
	if r == nil || service == nil {
		return command.ErrInvalid
	}
	group := r.Group("/api/v1")
	group.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	group.POST("/wishes/:id/applications", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in wish.ApplicationInput
		if !interactionJSON(c, []string{"message", "wish_content_version"}, &in) || in.WishContentVersion < 1 {
			returnInvalid(c)
			return
		}
		out, e := service.Apply(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	group.POST("/applications/:id/withdraw", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in struct {
			Version int64 `json:"expected_version"`
		}
		if !interactionJSON(c, []string{"expected_version"}, &in) || in.Version < 1 {
			returnInvalid(c)
			return
		}
		out, e := service.WithdrawApplication(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in.Version)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.POST("/applications/:id/accept", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in wish.AcceptInput
		if !interactionJSON(c, []string{"expected_version", "wish_expected_version"}, &in) || in.ExpectedVersion < 1 || in.WishExpectedVersion < 1 {
			returnInvalid(c)
			return
		}
		out, e := service.AcceptApplication(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.POST("/wishes/:id/rollback", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var in wish.RollbackInput
		if !interactionJSON(c, []string{"expected_version", "target", "refresh_expiry"}, &in) || in.ExpectedVersion < 1 || in.Target != wish.Matching && in.Target != wish.Published {
			returnInvalid(c)
			return
		}
		out, e := service.Rollback(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if e != nil {
			adminError(c, e)
			return
		}
		wishMutationResult(c, http.StatusOK, out)
	})
	for _, path := range []string{"/me/applications", "/wishes/:id/applications"} {
		own := path == "/me/applications"
		group.GET(path, func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			q, e := applicationQuery(c, own)
			if e != nil {
				adminError(c, e)
				return
			}
			var out wish.ApplicationPage
			if own {
				out, e = service.OwnApplications(c.Request.Context(), token, c.GetString("request_id"), q)
			} else {
				out, e = service.Applications(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), q)
			}
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, http.StatusOK, out)
		})
	}
	return nil
}

func returnInvalid(c *gin.Context) { adminError(c, command.ErrInvalid) }
func interactionJSON(c *gin.Context, names []string, target any) bool {
	data, fields, e := readWishJSON(c, names)
	if e != nil {
		return false
	}
	defer clear(data)
	for _, name := range names {
		if fields[name] == nil {
			return false
		}
	}
	return json.Unmarshal(data, target) == nil
}
func applicationQuery(c *gin.Context, own bool) (q wish.ApplicationQuery, err error) {
	q.Limit = 20
	allowed := []string{"cursor", "limit"}
	if own {
		allowed = append(allowed, "status")
	}
	for name, values := range c.Request.URL.Query() {
		if !slices.Contains(allowed, name) || len(values) != 1 || values[0] == "" {
			return q, command.ErrInvalid
		}
	}
	q.Status = c.Query("status")
	q.Cursor = c.Query("cursor")
	if value, ok := c.GetQuery("limit"); ok {
		q.Limit, err = strconv.Atoi(value)
		if err != nil || q.Limit < 1 || q.Limit > 100 {
			return q, command.ErrInvalid
		}
	}
	return q, nil
}
