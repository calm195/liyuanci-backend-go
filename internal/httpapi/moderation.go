package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
)

// ModerationService 暴露词库管理和普通审核案件操作。
type ModerationService interface {
	Wordlists(context.Context, string, string, string, int) (moderation.WordlistPage, error)
	Wordlist(context.Context, string, string, string) (moderation.WordlistDetail, error)
	CreateWordlist(context.Context, string, string, string, moderation.WordlistInput) (moderation.Wordlist, error)
	PublishWordlist(context.Context, string, string, string, string, moderation.WordlistPublish) (moderation.Wordlist, error)
	BuiltinWordlistEntries(context.Context, string, string, string, moderation.BuiltinWordlistFilter) (moderation.BuiltinWordlistPage, error)
	BuiltinUpstreamChanges(context.Context, string, string, string, int, int) (moderation.BuiltinUpstreamChangesPage, error)
	CreateBuiltinWordlist(context.Context, string, string, string, moderation.BuiltinWordlistInput) (moderation.Wordlist, error)
	PreviewWordlist(context.Context, string, string, string, string) (moderation.WordlistPreview, error)
	RestoreWordlist(context.Context, string, string, string, string, moderation.WordlistRestore) (moderation.Wordlist, error)
	Cases(context.Context, string, string, string, int) (moderation.Page, error)
	QueryCases(context.Context, string, string, moderation.CaseQuery) (moderation.Page, error)
	Case(context.Context, string, string, string) (moderation.Case, error)
	Decide(context.Context, string, string, string, string, moderation.Decision) (moderation.Outcome, error)
}

func moderationPage(c *gin.Context) (string, int, bool) {
	q := c.Request.URL.Query()
	limit := 20
	for key, values := range q {
		if (key != "cursor" && key != "limit") || len(values) != 1 || values[0] == "" {
			adminError(c, command.ErrInvalid)
			return "", 0, false
		}
	}
	if raw := q.Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			adminError(c, command.ErrInvalid)
			return "", 0, false
		}
		limit = n
	}
	return q.Get("cursor"), limit, true
}

// moderationJSON 在既有严格解析之外强制字段名精确匹配，拒绝Go JSON的大小写宽松映射。
func moderationJSON(c *gin.Context, target any, allowed ...string) error {
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil || !utf8.Valid(raw) {
		return command.ErrInvalid
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if e := strictJSON(c, &fields); e != nil {
		return e
	}
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
	}
	for key := range fields {
		if !set[key] {
			return command.ErrInvalid
		}
	}
	b, e := json.Marshal(fields)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, target)
}

// RegisterModeration 显式装配词库管理和普通审核后台路由。
func RegisterModeration(r *gin.Engine, s ModerationService) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	g := r.Group("/api/v1/admin")
	g.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, requestBodyLimit(c))
		c.Next()
	})
	g.GET("/wordlists", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		cursor, limit, ok := moderationPage(c)
		if !ok {
			return
		}
		out, e := s.Wordlists(c.Request.Context(), token, c.GetString("request_id"), cursor, limit)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.GET("/wordlists/:id", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := s.Wordlist(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.POST("/wordlists/versions", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input moderation.WordlistInput
		if moderationJSON(c, &input, "kind", "entries", "expected_version", "disabled_entries", "rules") != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := s.CreateWordlist(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 201, out)
	})
	g.POST("/wordlists/:id/publish", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input moderation.WordlistPublish
		if moderationJSON(c, &input, "expected_version", "expected_head_version") != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := s.PublishWordlist(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	registerWordlistTools(g, s)
	g.GET("/moderation-tasks", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		query, ok := moderationCasesQuery(c)
		if !ok {
			return
		}
		out, e := s.QueryCases(c.Request.Context(), token, c.GetString("request_id"), query)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.GET("/moderation-tasks/:id", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := s.Case(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.POST("/moderation-tasks/:id/decision", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input moderation.Decision
		if moderationJSON(c, &input, "decision", "reason", "revision_suggestion", "expected_version") != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := s.Decide(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	return nil
}
