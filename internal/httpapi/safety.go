package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
)

// SafetyAPI 提供固定安全治理命令及当前主体可见投影。
type SafetyAPI interface {
	Execute(context.Context, string, string, string, string, string, safety.Input) (any, error)
	List(context.Context, string, string, string, safety.Query) (safety.Page, error)
	Detail(context.Context, string, string, string, string) (any, error)
}

// RegisterSafety 显式装配学生与后台独立服务，基础健康模式不注册这些业务端点。
func RegisterSafety(r *gin.Engine, student, admin SafetyAPI) error {
	if r == nil || student == nil || admin == nil {
		return command.ErrInvalid
	}
	if users, ok := admin.(AdminUsersAPI); ok {
		if err := RegisterAdminUsers(r, users); err != nil {
			return err
		}
	}
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	writes := []struct {
		op, method, path   string
		required, optional []string
		status             int
		admin              bool
	}{
		{"block", "POST", "/me/blocks", []string{"target_user_id"}, nil, 200, false},
		{"unblock", "DELETE", "/me/blocks/:id", []string{"expected_version"}, nil, 204, false},
		{"report", "POST", "/reports", []string{"target_kind", "id", "type_code"}, []string{"description"}, 202, false},
		{"appeal", "POST", "/me/appeals", []string{"penalty_id", "body"}, nil, 202, false},
		{"display", "POST", "/me/warnings/:id/display", []string{"expected_version"}, nil, 200, false},
		{"ack", "POST", "/me/warnings/:id/ack", []string{"expected_version"}, nil, 200, false},
		{"exception_exit", "POST", "/wishes/:id/exception-exit", []string{"expected_version"}, nil, 200, false},
		{"report_decision", "POST", "/admin/reports/:id/decision", []string{"decision", "reason", "expected_version"}, nil, 200, true},
		{"offline", "POST", "/admin/contents/:kind/:id/offline", []string{"reason", "expected_version"}, nil, 200, true},
		{"release", "POST", "/admin/contents/:kind/:id/release-restriction", []string{"reason", "expected_version"}, nil, 200, true},
		{"penalty", "POST", "/admin/users/:id/penalties", []string{"violation_ref", "content_ref", "content_version", "level", "reason", "expected_version"}, nil, 200, true},
		{"appeal_decision", "POST", "/admin/appeals/:id/decision", []string{"decision", "reason", "expected_version"}, nil, 200, true},
	}
	for _, route := range writes {
		g.Handle(route.method, route.path, func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			if route.op == "block" || route.op == "unblock" {
				if guard, ok := student.(interface {
					LegacyBlocks(context.Context, string) error
				}); ok {
					if e := guard.LegacyBlocks(c.Request.Context(), token); e != nil {
						adminError(c, e)
						return
					}
				}
			}
			key, ok := idempotency(c)
			if !ok {
				return
			}
			allowed := append(append([]string(nil), route.required...), route.optional...)
			data, fields, e := readWishJSON(c, allowed)
			if e != nil {
				adminError(c, e)
				return
			}
			defer clear(data)
			for _, name := range route.required {
				if fields[name] == nil {
					returnInvalid(c)
					return
				}
			}
			var in safety.Input
			if json.Unmarshal(data, &in) != nil {
				returnInvalid(c)
				return
			}
			if route.op == "offline" || route.op == "release" {
				in.TargetKind = c.Param("kind")
			}
			service := student
			if route.admin {
				service = admin
			}
			out, e := service.Execute(c.Request.Context(), token, key, c.GetString("request_id"), route.op, c.Param("id"), in)
			if e != nil {
				adminError(c, e)
				return
			}
			if route.status == 204 {
				c.Status(http.StatusNoContent)
				return
			}
			adminResult(c, route.status, out)
		})
	}
	for _, route := range []struct {
		op, path string
		admin    bool
	}{
		{"blocks", "/me/blocks", false}, {"warnings", "/me/warnings", false}, {"appeals", "/me/appeals", false}, {"report_types", "/report-types", false}, {"reports", "/admin/reports", true}, {"admin_appeals", "/admin/appeals", true},
	} {
		g.GET(route.path, func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			if route.op == "blocks" {
				if guard, ok := student.(interface {
					LegacyBlocks(context.Context, string) error
				}); ok {
					if e := guard.LegacyBlocks(c.Request.Context(), token); e != nil {
						adminError(c, e)
						return
					}
				}
			}
			q, e := chatQuery(c, false)
			if e != nil {
				adminError(c, e)
				return
			}
			service := student
			if route.admin {
				service = admin
			}
			out, e := service.List(c.Request.Context(), token, c.GetString("request_id"), route.op, safety.Query{Cursor: q.Cursor, Limit: q.Limit})
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, 200, out)
		})
	}
	for _, route := range []struct{ op, path string }{{"report_detail", "/admin/reports/:id"}, {"appeal_detail", "/admin/appeals/:id"}} {
		g.GET(route.path, func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			if len(c.Request.URL.Query()) != 0 {
				returnInvalid(c)
				return
			}
			out, e := admin.Detail(c.Request.Context(), token, c.GetString("request_id"), route.op, c.Param("id"))
			if e != nil {
				adminError(c, e)
				return
			}
			adminResult(c, 200, out)
		})
	}
	return nil
}
