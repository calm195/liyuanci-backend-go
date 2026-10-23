package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
)

// CrisisAPI 只提供案件级危机服务，不提供私聊搜索或批量身份导出。
type CrisisAPI interface {
	CrisisWork(context.Context, string, string, string) (moderation.CrisisWorkView, error)
	CrisisTimelineDetails(context.Context, string, string, string) ([]moderation.CrisisTimeline, error)
	CrisisCases(context.Context, string, string, string, int) (moderation.CrisisPage, error)
	CrisisCase(context.Context, string, string, string) (moderation.CrisisView, error)
	CrisisEvidence(context.Context, string, string, string) (moderation.EvidenceView, error)
	ActOnCrisis(context.Context, string, string, string, string, moderation.CrisisCommand, bool) (moderation.CrisisView, error)
	EscalateCrisis(context.Context, string, string, string, string, string, moderation.CrisisCommand) (moderation.CrisisView, error)
}

// RegisterCrisis 显式注册危机和证据路由，所有响应禁止缓存。
func RegisterCrisis(r *gin.Engine, s CrisisAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	g := r.Group("/api/v1/admin")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	g.GET("/crisis-cases", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, e := chatQuery(c, false)
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := s.CrisisCases(c.Request.Context(), token, c.GetString("request_id"), q.Cursor, q.Limit)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	for _, route := range []struct{ path, kind string }{{"/crisis-cases/:id", "case"}, {"/crisis-cases/:id/timeline", "timeline"}, {"/crisis-cases/:id/work", "work"}, {"/evidence/:id", "evidence"}, {"/evidence/:id/asset", "asset"}} {
		g.GET(route.path, func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			if len(c.Request.URL.Query()) != 0 {
				returnInvalid(c)
				return
			}
			var out any
			var e error
			if route.kind == "work" {
				out, e = s.CrisisWork(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
			} else if route.kind == "timeline" {
				out, e = s.CrisisTimelineDetails(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
			} else if route.kind == "case" {
				out, e = s.CrisisCase(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
			} else {
				out, e = s.CrisisEvidence(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
			}
			if e != nil {
				adminError(c, e)
				return
			}
			// 文字危机证据不复制无关学生卡；本案件无涉案资产时始终404。
			if route.kind == "asset" {
				adminError(c, command.ErrNotFound)
				return
			}
			adminResult(c, http.StatusOK, out)
		})
	}
	for _, route := range []struct {
		path, source  string
		falsePositive bool
	}{{"/crisis-cases/:id/actions", "", false}, {"/crisis-cases/:id/resolve-false-positive", "", true}, {"/moderation-tasks/:id/escalate-crisis", "task", false}, {"/reports/:id/escalate-crisis", "report", false}} {
		g.POST(route.path, func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			key, ok := idempotency(c)
			if !ok {
				return
			}
			fields := []string{"reason", "expected_version"}
			if route.source == "" && !route.falsePositive {
				fields = append(fields, "action")
			}
			var in moderation.CrisisCommand
			if !interactionJSON(c, fields, &in) {
				returnInvalid(c)
				return
			}
			var out moderation.CrisisView
			var e error
			if route.source != "" {
				out, e = s.EscalateCrisis(c.Request.Context(), token, key, c.GetString("request_id"), route.source, c.Param("id"), in)
			} else {
				out, e = s.ActOnCrisis(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in, route.falsePositive)
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
