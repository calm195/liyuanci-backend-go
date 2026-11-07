package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
)

const builtinWordlistDraftRoute = "/wordlists/builtin/versions"

func requestBodyLimit(c *gin.Context) int64 {
	if c.Request.Method == http.MethodPost && c.FullPath() == "/api/v1/admin"+builtinWordlistDraftRoute {
		return 2 << 20
	}
	return 1 << 20
}

func registerWordlistTools(g *gin.RouterGroup, s ModerationService) {
	g.GET("/wordlists/:id/entries", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		filter, e := parseBuiltinWordlistFilter(c, true)
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := s.BuiltinWordlistEntries(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), filter)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.GET("/wordlists/:id/upstream-changes", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		filter, e := parseBuiltinWordlistFilter(c, false)
		if e != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := s.BuiltinUpstreamChanges(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), filter.Offset, filter.Limit)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.POST(builtinWordlistDraftRoute, func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		input, e := parseBuiltinWordlistInput(c)
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := s.CreateBuiltinWordlist(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 201, out)
	})
	g.POST("/wordlists/:id/restore", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input moderation.WordlistRestore
		if moderationJSON(c, &input, "expected_head_version") != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := s.RestoreWordlist(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), input)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 201, out)
	})
	g.POST("/wordlists/:id/preview", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		var input struct {
			Text string `json:"text"`
		}
		if moderationJSON(c, &input, "text") != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := s.PreviewWordlist(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), input.Text)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
}

func parseBuiltinWordlistFilter(c *gin.Context, allowFilters bool) (moderation.BuiltinWordlistFilter, error) {
	filter := moderation.BuiltinWordlistFilter{Limit: 50}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return filter, command.ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 || !allowFilters && key != "offset" && key != "limit" {
			return filter, command.ErrInvalid
		}
		value := values[0]
		switch key {
		case "q":
			filter.Query = value
		case "label":
			filter.Label = value
		case "disposition":
			filter.Disposition = value
		case "enabled":
			if value != "true" && value != "false" {
				return filter, command.ErrInvalid
			}
			enabled := value == "true"
			filter.Enabled = &enabled
		case "offset", "limit":
			number, e := strconv.Atoi(value)
			if e != nil {
				return filter, command.ErrInvalid
			}
			if key == "offset" {
				filter.Offset = number
			} else {
				filter.Limit = number
			}
		default:
			return filter, command.ErrInvalid
		}
	}
	return filter, nil
}

func parseBuiltinWordlistInput(c *gin.Context) (out moderation.BuiltinWordlistInput, err error) {
	// 逐项解析嵌套覆盖，拒绝大小写变体、重复字段及显式 null。
	var wire struct {
		ExpectedVersion int64             `json:"expected_version"`
		RefreshUpstream bool              `json:"refresh_upstream"`
		Overrides       []json.RawMessage `json:"overrides"`
		LabelOverrides  []json.RawMessage `json:"label_overrides"`
	}
	if moderationJSON(c, &wire, "expected_version", "overrides", "label_overrides", "refresh_upstream") != nil {
		return out, command.ErrInvalid
	}
	out.ExpectedVersion, out.RefreshUpstream = wire.ExpectedVersion, wire.RefreshUpstream
	for _, group := range []struct {
		field string
		raw   []json.RawMessage
		out   *[]moderation.BuiltinWordlistOverride
	}{
		{"id", wire.Overrides, &out.Overrides},
		{"label", wire.LabelOverrides, &out.LabelOverrides},
	} {
		for _, raw := range group.raw {
			var override moderation.BuiltinWordlistOverride
			if err = moderationJSONBody(c, raw, &override, group.field, "enabled", "disposition"); err != nil {
				return out, command.ErrInvalid
			}
			*group.out = append(*group.out, override)
		}
	}
	return out, nil
}

func moderationJSONBody(c *gin.Context, raw []byte, target any, allowed ...string) error {
	body := c.Request.Body
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	defer func() { c.Request.Body = body }()
	return moderationJSON(c, target, allowed...)
}
