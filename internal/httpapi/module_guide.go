package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moduleguide"
)

// ModuleGuideAPI 由装配方注入，领域层复核会话、岗位、版本及幂等回执。
type ModuleGuideAPI interface {
	ReadAdmin(context.Context, string, string) (moduleguide.AdminGuide, error)
	Read(context.Context, string, string) (moduleguide.Guide, error)
	Save(context.Context, string, string, string, moduleguide.SaveInput) (moduleguide.AdminGuide, error)
	Publish(context.Context, string, string, string, moduleguide.PublishInput) (moduleguide.AdminGuide, error)
	Acknowledge(context.Context, string, string, string, moduleguide.AckInput) (moduleguide.Guide, error)
}

// RegisterModuleGuide 显式注册说明配置、发布及本人确认接口。
func RegisterModuleGuide(r *gin.Engine, s ModuleGuideAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	group := r.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		if len(c.Request.URL.Query()) != 0 {
			returnInvalid(c)
			c.Abort()
			return
		}
		c.Next()
	})
	group.GET("/admin/module-guide", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := s.ReadAdmin(c.Request.Context(), token, c.GetString("request_id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	group.GET("/me/module-guide", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := s.Read(c.Request.Context(), token, c.GetString("request_id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	group.PUT("/admin/module-guide", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input moduleguide.SaveInput
		if strictJSON(c, &input) != nil {
			returnInvalid(c)
			return
		}
		out, e := s.Save(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	group.POST("/admin/module-guide/publish", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input moduleguide.PublishInput
		if strictJSON(c, &input) != nil {
			returnInvalid(c)
			return
		}
		out, e := s.Publish(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	group.PUT("/me/module-guide/ack", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input moduleguide.AckInput
		if strictJSON(c, &input) != nil {
			returnInvalid(c)
			return
		}
		out, e := s.Acknowledge(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	return nil
}
