package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// AdminService 注入独立后台身份服务，处理器只负责协议验证和稳定状态映射。
type AdminService interface {
	ListAccounts(context.Context, string, string, string, int) (adminauth.AccountPage, error)
	ChangeRoles(context.Context, string, string, string, string, adminauth.RolesInput) (adminauth.Account, error)
	Create(context.Context, string, string, string, adminauth.CreateInput) (adminauth.Account, error)
	Login(context.Context, string, string, string, string) (adminauth.Session, error)
	ChangePassword(context.Context, string, string, string, adminauth.PasswordInput) error
	Logout(context.Context, string, string, string) error
}

// RegisterAdmin 仅由显式装配调用；默认路由仍只有健康端点，不能用nil服务开放模拟入口。
func RegisterAdmin(r *gin.Engine, service AdminService) error {
	if r == nil || service == nil {
		return adminauth.ErrInvalid
	}
	group := r.Group("/api/v1/admin")
	group.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	group.GET("/accounts", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		query := c.Request.URL.Query()
		for name, values := range query {
			if (name != "cursor" && name != "limit") || len(values) != 1 {
				adminError(c, adminauth.ErrInvalid)
				return
			}
		}
		limit := 20
		if values, exists := query["limit"]; exists {
			n, e := strconv.Atoi(values[0])
			if e != nil || n < 1 || n > 50 {
				adminError(c, adminauth.ErrInvalid)
				return
			}
			limit = n
		}
		out, err := service.ListAccounts(c.Request.Context(), token, query.Get("cursor"), c.GetString("request_id"), limit)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.PUT("/accounts/:id/roles", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		target := c.Param("id")
		id, e := uuid.Parse(target)
		var input adminauth.RolesInput
		if e != nil || id.String() != target || strictJSON(c, &input) != nil || input.Roles == nil || input.ExpectedVersion < 1 || strings.TrimSpace(input.Reason) == "" {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		out, err := service.ChangeRoles(c.Request.Context(), token, key, c.GetString("request_id"), target, input)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.POST("/sessions", func(c *gin.Context) {
		var input struct {
			LoginName string `json:"login_name"`
			Password  string `json:"password"`
		}
		if strictJSON(c, &input) != nil || strings.TrimSpace(input.LoginName) == "" || input.Password == "" {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		out, err := service.Login(c.Request.Context(), input.LoginName, input.Password, c.ClientIP(), c.GetString("request_id"))
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.POST("/accounts", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input adminauth.CreateInput
		if strictJSON(c, &input) != nil || input.Roles == nil {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		out, err := service.Create(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusCreated, out)
	})
	group.POST("/password", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input adminauth.PasswordInput
		if strictJSON(c, &input) != nil {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		if err := service.ChangePassword(c.Request.Context(), token, key, c.GetString("request_id"), input); err != nil {
			adminError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	group.DELETE("/session", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		if err := service.Logout(c.Request.Context(), token, key, c.GetString("request_id")); err != nil {
			adminError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	return nil
}

// strictJSON 拒绝重复/未知字段和尾随值，避免幂等摘要与客户端所见请求含义不一致。
func strictJSON(c *gin.Context, target any) error {
	if c.ContentType() != "application/json" {
		return adminauth.ErrInvalid
	}
	decoder := json.NewDecoder(c.Request.Body)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return adminauth.ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields[name] != nil {
			return adminauth.ErrInvalid
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || string(raw) == "null" {
			return adminauth.ErrInvalid
		}
		fields[name] = raw
	}
	if _, err = decoder.Token(); err != nil {
		return adminauth.ErrInvalid
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return adminauth.ErrInvalid
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return adminauth.ErrInvalid
	}
	typed := json.NewDecoder(strings.NewReader(string(body)))
	typed.DisallowUnknownFields()
	return typed.Decode(target)
}

func bearer(c *gin.Context) (string, bool) {
	values := c.Request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer ")) == "" {
		adminError(c, adminauth.ErrUnauthenticated)
		return "", false
	}
	return strings.TrimPrefix(values[0], "Bearer "), true
}

func idempotency(c *gin.Context) (string, bool) {
	values := c.Request.Header.Values("Idempotency-Key")
	if len(values) != 1 || len(values[0]) < 1 || len(values[0]) > 128 {
		adminError(c, adminauth.ErrInvalid)
		return "", false
	}
	return values[0], true
}

func adminResult(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"code": 0, "message": "成功", "data": data, "request_id": c.GetString("request_id")})
}

func adminError(c *gin.Context, err error) {
	status, code := http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"
	switch {
	case errors.Is(err, adminauth.ErrInvalid):
		status, code = 400, "VALIDATION_FAILED"
	case errors.Is(err, adminauth.ErrUnauthenticated):
		status, code = 401, "UNAUTHENTICATED"
	case errors.Is(err, adminauth.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(err, command.ErrWarningWait):
		status, code = 409, "WARNING_WAIT_REQUIRED"
	case errors.Is(err, command.ErrAssetNotReady):
		status, code = 409, "ASSET_NOT_READY"
	case errors.Is(err, command.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(err, command.ErrVersionConflict):
		status, code = 409, "VERSION_CONFLICT"
	case errors.Is(err, command.ErrActiveWishLimit):
		status, code = 409, "ACTIVE_WISH_LIMIT"
	case errors.Is(err, command.ErrDuplicateApplication):
		status, code = 409, "DUPLICATE_APPLICATION"
	case errors.Is(err, command.ErrBlockedInteraction):
		status, code = 409, "BLOCKED_INTERACTION"
	case errors.Is(err, command.ErrReviewPending):
		status, code = 409, "REVIEW_PENDING"
	case errors.Is(err, adminauth.ErrConflict):
		status, code = 409, "STATE_CONFLICT"
	case errors.Is(err, command.ErrIdempotencyConflict):
		status, code = 409, "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, adminauth.ErrRateLimited):
		status, code = 429, "RATE_LIMITED"
	case errors.Is(err, command.ErrInternal):
		status, code = 500, "INTERNAL_ERROR"
	}
	c.AbortWithStatusJSON(status, gin.H{"code": status, "message": "请求未完成", "error_code": code, "request_id": c.GetString("request_id")})
}
