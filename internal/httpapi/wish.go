package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// WishCreator 仅暴露已装配的草稿创建能力，不允许客户端指定作者、状态或审核结果。
type WishCreator interface {
	Create(context.Context, string, string, string, wish.ContentInput) (wish.CreatedDraft, error)
}

// RegisterWishCreation 在带请求完成日志/恢复中间件的New路由上显式启用草稿创建，不启用其他心愿操作。
func RegisterWishCreation(r *gin.Engine, service WishCreator) error {
	if r == nil || service == nil {
		return command.ErrInvalid
	}
	r.POST("/api/v1/wishes", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		input, err := decodeWishInput(c)
		if err != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, err := service.Create(c.Request.Context(), token, key, c.GetString("request_id"), input)
		if err != nil {
			adminError(c, err)
			return
		}
		// 只投影当前已实现能力；编辑、送审、上架尚未装配，不能因创建成功向客户端授予动作。
		adminResult(c, http.StatusCreated, gin.H{
			"id": out.ID, "version": out.Version, "content_version": out.ContentVersion,
			"current_state": out.State, "allowed_actions": []string{},
		})
	})
	return nil
}

// wishObject 严格保留字段名大小写，拒绝重复键、null及尾随值；嵌套标签应用相同规则。
func wishObject(data []byte, allowed []string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, command.ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		name, ok := token.(string)
		if err != nil || !ok || fields[name] != nil || !slices.Contains(allowed, name) {
			return nil, command.ErrInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, command.ErrInvalid
		}
		fields[name] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, command.ErrInvalid
	}
	if !errors.Is(d.Decode(new(any)), io.EOF) {
		return nil, command.ErrInvalid
	}
	return fields, nil
}

func readWishJSON(c *gin.Context, allowed []string) (data []byte, fields map[string]json.RawMessage, err error) {
	if c.ContentType() != "application/json" {
		return nil, nil, command.ErrInvalid
	}
	data, err = io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil || !utf8.Valid(data) {
		clear(data)
		return nil, nil, command.ErrInvalid
	}
	fields, err = wishObject(data, allowed)
	if err != nil {
		clear(data)
		return nil, nil, err
	}
	if tags := fields["identity_tags"]; tags != nil {
		if _, err = wishObject(tags, []string{"campus", "grade", "gender"}); err != nil {
			clear(data)
			return nil, nil, err
		}
	}
	return data, fields, nil
}

func decodeWishInput(c *gin.Context) (out wish.ContentInput, err error) {
	data, fields, err := readWishJSON(c, []string{"category", "body", "campus", "identity_tags", "reward_method", "duration_days"})
	if err != nil {
		return out, err
	}
	defer clear(data)
	for _, required := range []string{"category", "body", "campus"} {
		if fields[required] == nil {
			return out, command.ErrInvalid
		}
	}
	if json.Unmarshal(data, &out) != nil {
		return wish.ContentInput{}, command.ErrInvalid
	}
	return out, nil
}
