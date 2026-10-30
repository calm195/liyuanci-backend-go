package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/url"
	"slices"
	"strings"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/resourceplaza"
	"github.com/gin-gonic/gin"
)

// ResourcesAPI 是资料业务的完整读写入口；生产装配注入受限数据库服务。
type ResourcesAPI interface {
	Read(context.Context, string, string, string, string, resourceplaza.Query, bool) (any, error)
	Write(context.Context, string, string, string, string, string, resourceplaza.Input) (any, error)
}

// RegisterResources 显式装配十五个资料接口，基础健康模式保持无业务路由。
func RegisterResources(r *gin.Engine, student, admin ResourcesAPI) error {
	if r == nil || student == nil || admin == nil {
		return command.ErrInvalid
	}
	routes := []struct {
		method, path, op string
		admin            bool
	}{
		{"GET", "/resource-categories", "categories", false}, {"GET", "/resources", "list", false}, {"GET", "/resources/:id", "detail", false}, {"GET", "/resources/:id/download-info", "download", false}, {"PUT", "/resources/:id/feedback", "feedback", false},
		{"GET", "/admin/resources", "list", true}, {"GET", "/admin/resources/:id", "detail", true}, {"POST", "/admin/resources", "create", true}, {"PUT", "/admin/resources/:id/content", "update", true}, {"POST", "/admin/resources/:id/publish", "publish", true}, {"POST", "/admin/resources/:id/offline", "offline", true},
		{"GET", "/admin/resource-categories", "categories", true}, {"GET", "/admin/resource-categories/:id", "category_detail", true}, {"POST", "/admin/resource-categories", "category_create", true}, {"PUT", "/admin/resource-categories/:id", "category_update", true},
	}
	for _, rt := range routes {
		r.Handle(rt.method, "/api/v1"+rt.path, func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			for _, h := range []string{"Authorization", "Content-Type", "Idempotency-Key"} {
				if len(c.Request.Header.Values(h)) > 1 {
					resourceError(c, command.ErrInvalid)
					return
				}
			}
			token, ok := bearer(c)
			if !ok {
				return
			}
			id := c.Param("id")
			var e error
			if id != "" {
				id, e = resourceplaza.ID(id)
				if e != nil {
					resourceError(c, e)
					return
				}
			}
			q, e := resourceQuery(c.Request.URL.RawQuery, rt.op, rt.admin)
			if e != nil {
				resourceError(c, e)
				return
			}
			service := student
			if rt.admin {
				service = admin
			}
			var out any
			status := 200
			if rt.method == "GET" {
				b, e := io.ReadAll(io.LimitReader(c.Request.Body, 1))
				if e != nil || len(b) != 0 {
					resourceError(c, command.ErrInvalid)
					return
				}
				out, e = service.Read(c.Request.Context(), token, c.GetString("request_id"), rt.op, id, q, rt.admin)
				if e != nil {
					resourceError(c, e)
					return
				}
			} else {
				media, params, e := mime.ParseMediaType(c.GetHeader("Content-Type"))
				if e != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && strings.ToLower(params["charset"]) != "utf-8") {
					resourceHTTPError(c, 415, "UNSUPPORTED_MEDIA_TYPE")
					return
				}
				raw, e := io.ReadAll(io.LimitReader(c.Request.Body, 65537))
				if e != nil {
					resourceError(c, command.ErrInvalid)
					return
				}
				if len(raw) > 65536 {
					resourceHTTPError(c, 413, "PAYLOAD_TOO_LARGE")
					return
				}
				key := c.GetHeader("Idempotency-Key")
				if len(c.Request.Header.Values("Idempotency-Key")) != 1 || len(key) < 1 || len(key) > 128 {
					resourceError(c, command.ErrInvalid)
					return
				}
				for _, v := range key {
					if v < 33 || v > 126 {
						resourceError(c, command.ErrInvalid)
						return
					}
				}
				in, e := resourceplaza.ParseInput(rt.op, raw)
				if e != nil {
					resourceError(c, e)
					return
				}
				out, e = service.Write(c.Request.Context(), token, c.GetString("request_id"), key, rt.op, id, in)
				if e != nil {
					resourceError(c, e)
					return
				}
				if rt.op == "create" || rt.op == "category_create" {
					status = 201
				}
			}
			adminResult(c, status, out)
		})
	}
	return nil
}
func resourceQuery(raw, op string, admin bool) (q resourceplaza.Query, err error) {
	values, e := url.ParseQuery(raw)
	if e != nil {
		return q, command.ErrInvalid
	}
	size := 0
	q.Limit = 20
	if op == "categories" {
		q.Limit = 100
	}
	allowed := []string{}
	if op == "list" {
		allowed = []string{"q", "category_id", "cursor", "limit"}
		if admin {
			allowed = append(allowed, "status")
		}
	} else if op == "categories" {
		allowed = []string{"cursor", "limit"}
		if admin {
			allowed = append(allowed, "enabled")
		}
	} else if op == "download" {
		allowed = []string{"expected_version"}
	}
	for k, v := range values {
		if len(v) != 1 || !slices.Contains(allowed, k) {
			return q, command.ErrInvalid
		}
		size += len(k) + len(v[0])
		if size > 8192 {
			return q, command.ErrInvalid
		}
		if v[0] == "" && k != "q" {
			return q, command.ErrInvalid
		}
		switch k {
		case "q":
			q.Q, e = resourceplaza.Text("q", v[0])
			q.Q = resourceplaza.Fold(q.Q)
		case "category_id":
			q.CategoryID, e = resourceplaza.ID(v[0])
		case "status":
			if !slices.Contains([]string{"draft", "published", "offline"}, v[0]) {
				e = command.ErrInvalid
			}
			q.Status = v[0]
		case "enabled":
			if v[0] != "true" && v[0] != "false" {
				e = command.ErrInvalid
			}
			q.Enabled = v[0]
		case "cursor":
			if len(v[0]) > 2048 {
				e = command.ErrInvalid
			}
			q.Cursor = v[0]
		case "expected_version":
			q.ExpectedVersion, e = resourceplaza.Integer(v[0], false)
		case "limit":
			var n int64
			n, e = resourceplaza.Integer(v[0], false)
			max := int64(50)
			if op == "categories" {
				max = 100
			}
			if n > max {
				e = command.ErrInvalid
			}
			q.Limit = int(n)
		}
		if e != nil {
			return q, e
		}
	}
	if op == "download" && q.ExpectedVersion == 0 {
		return q, command.ErrInvalid
	}
	return
}
func resourceHTTPError(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"code": status, "message": "资料请求未完成", "error_code": code, "request_id": c.GetString("request_id")})
}
func resourceError(c *gin.Context, e error) {
	status, code := 503, "DEPENDENCY_UNAVAILABLE"
	for _, v := range []struct {
		err    error
		status int
		code   string
	}{{command.ErrInvalid, 400, "VALIDATION_FAILED"}, {command.ErrUnauthenticated, 401, "UNAUTHENTICATED"}, {command.ErrForbidden, 403, "FORBIDDEN"}, {command.ErrVerificationRequired, 403, "VERIFICATION_REQUIRED"}, {command.ErrNotFound, 404, "NOT_FOUND"}, {command.ErrVersionConflict, 409, "VERSION_CONFLICT"}, {command.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"}, {command.ErrConflict, 409, "STATE_CONFLICT"}, {resourceplaza.ErrCategoryInUse, 409, "CATEGORY_IN_USE"}, {resourceplaza.ErrCategoryName, 409, "CATEGORY_NAME_CONFLICT"}, {command.ErrInternal, 500, "INTERNAL_ERROR"}} {
		if errors.Is(e, v.err) {
			status, code = v.status, v.code
			break
		}
	}
	resourceHTTPError(c, status, code)
}
