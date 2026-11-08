package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/dictionary"
)

// DictionaryAPI 由领域服务复核权限、版本和重复命令。
type DictionaryAPI interface {
	ReadReportTypes(context.Context, string, string) (dictionary.ReportTypesSnapshot, error)
	SaveReportType(context.Context, string, string, string, string, dictionary.SaveReportTypeInput) (dictionary.ReportTypesSnapshot, error)
	Read(context.Context, string) (dictionary.Snapshot, error)
	ReadAdmin(context.Context, string, string) (dictionary.Snapshot, error)
	Save(context.Context, string, string, string, string, string, dictionary.SaveInput) (dictionary.Snapshot, error)
}

// RegisterDictionaries 注册无个人信息的公开字典及受控管理接口。
func RegisterDictionaries(r *gin.Engine, s DictionaryAPI) error {
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
	group.GET("/dictionaries", func(c *gin.Context) {
		out, e := s.Read(c.Request.Context(), c.GetString("request_id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	group.GET("/admin/dictionaries", func(c *gin.Context) {
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
	group.PUT("/admin/dictionaries/:type/:code", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input dictionary.SaveInput
		if strictJSON(c, &input) != nil {
			returnInvalid(c)
			return
		}
		out, e := s.Save(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("type"), c.Param("code"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	group.GET("/admin/report-types", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := s.ReadReportTypes(c.Request.Context(), token, c.GetString("request_id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	group.PUT("/admin/report-types/:code", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input dictionary.SaveReportTypeInput
		if strictJSON(c, &input) != nil {
			returnInvalid(c)
			return
		}
		out, e := s.SaveReportType(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("code"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	return nil
}
