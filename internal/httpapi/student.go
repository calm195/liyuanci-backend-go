package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
)

// StudentService 由装配方显式注入；不把后台身份服务或合成供应商自动用于学生。
type StudentService interface {
	Login(context.Context, string, string, string, string) (studentauth.Session, error)
	Current(context.Context, string, string) (studentauth.Me, error)
	UpdateProfile(context.Context, string, string, string, studentauth.ProfileInput) (studentauth.Me, error)
	Logout(context.Context, string, string, string) error
}

// RegisterStudent 仅在真实或明确隔离适配器显式装配时开放；普通启动仍无学生路由。
func RegisterStudent(r *gin.Engine, service StudentService) error {
	if r == nil || service == nil {
		return adminauth.ErrInvalid
	}
	group := r.Group("/api/v1")
	noStore := func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() }
	fail := func(c *gin.Context, err error) {
		if errors.Is(err, studentauth.ErrRateLimited) {
			adminError(c, adminauth.ErrRateLimited)
		} else {
			adminError(c, err)
		}
	}
	group.POST("/auth/wechat-sessions", noStore, func(c *gin.Context) {
		var input struct {
			Code            string `json:"code"`
			ClientRequestID string `json:"client_request_id"`
		}
		if strictJSON(c, &input) != nil || strings.TrimSpace(input.Code) == "" || input.ClientRequestID == "" {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		out, e := service.Login(c.Request.Context(), input.Code, input.ClientRequestID, c.ClientIP(), c.GetString("request_id"))
		if e != nil {
			fail(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.GET("/me", noStore, func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := service.Current(c.Request.Context(), token, c.GetString("request_id"))
		if e != nil {
			fail(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.PUT("/me/profile", noStore, func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input studentauth.ProfileInput
		if strictJSON(c, &input) != nil || strings.TrimSpace(input.Nickname) == "" || input.HideIdentityTags == nil || input.ExpectedVersion < 1 {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		out, e := service.UpdateProfile(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if e != nil {
			fail(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.DELETE("/auth/session", noStore, func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		if e := service.Logout(c.Request.Context(), token, key, c.GetString("request_id")); e != nil {
			fail(c, e)
			return
		}
		c.Status(http.StatusNoContent)
	})
	return nil
}
