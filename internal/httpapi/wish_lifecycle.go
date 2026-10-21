package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// WishesAPI 只包含心愿本域已实现的命令与查询，不提供审核决定或匹配操作。
type WishesAPI interface {
	WishCreator
	Update(context.Context, string, string, string, string, wish.UpdateInput) (wish.CreatedDraft, error)
	Submit(context.Context, string, string, string, string, int64, int64) (wish.CreatedDraft, error)
	Publish(context.Context, string, string, string, string, int64) (wish.CreatedDraft, error)
	Withdraw(context.Context, string, string, string, string, int64) (wish.CreatedDraft, error)
	Delete(context.Context, string, string, string, string, int64) error
	Detail(context.Context, string, string, string) (wish.Result, error)
	Own(context.Context, string, string, wish.Query) (wish.OwnPage, error)
	List(context.Context, string, wish.Query) (wish.PublicPage, error)
}

// RegisterWishes 显式注册完整本域入口，包含创建；不能与RegisterWishCreation重复注册同一路由。
func RegisterWishes(r *gin.Engine, service WishesAPI) error {
	if r == nil || service == nil {
		return command.ErrInvalid
	}
	if err := RegisterWishCreation(r, service); err != nil {
		return err
	}
	group := r.Group("/api/v1")
	// 可见性取决于当前资格和屏障，所有视图禁用响应缓存，失败和恢复同样保留请求完成日志。
	group.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	group.PUT("/wishes/:id/content", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		data, fields, err := readWishJSON(c, []string{"category", "body", "campus", "identity_tags", "reward_method", "duration_days", "refresh_expiry", "expected_version"})
		if err != nil {
			adminError(c, err)
			return
		}
		defer clear(data)
		for _, name := range []string{"category", "body", "campus", "refresh_expiry", "expected_version"} {
			if fields[name] == nil {
				adminError(c, command.ErrInvalid)
				return
			}
		}
		var in wish.UpdateInput
		if json.Unmarshal(data, &in) != nil || in.ExpectedVersion < 1 {
			adminError(c, command.ErrInvalid)
			return
		}
		out, err := service.Update(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), in)
		if err != nil {
			adminError(c, err)
			return
		}
		wishMutationResult(c, http.StatusOK, out)
	})
	for _, operation := range []string{"submit-review", "publish", "withdraw", "draft"} {
		op := operation
		handler := func(c *gin.Context) {
			token, ok := bearer(c)
			if !ok {
				return
			}
			key, ok := idempotency(c)
			if !ok {
				return
			}
			allowed := []string{"expected_version"}
			if op == "submit-review" {
				allowed = append(allowed, "content_version")
			}
			data, fields, err := readWishJSON(c, allowed)
			if err != nil {
				adminError(c, err)
				return
			}
			defer clear(data)
			var input struct {
				Version        int64 `json:"expected_version"`
				ContentVersion int64 `json:"content_version"`
			}
			if json.Unmarshal(data, &input) != nil || fields["expected_version"] == nil || input.Version < 1 || op == "submit-review" && (fields["content_version"] == nil || input.ContentVersion < 1) {
				adminError(c, command.ErrInvalid)
				return
			}
			ctx, rid, id := c.Request.Context(), c.GetString("request_id"), c.Param("id")
			if op == "draft" {
				if err = service.Delete(ctx, token, key, rid, id, input.Version); err != nil {
					adminError(c, err)
					return
				}
				c.Status(http.StatusNoContent)
				return
			}
			var out wish.CreatedDraft
			status := http.StatusOK
			switch op {
			case "submit-review":
				out, err = service.Submit(ctx, token, key, rid, id, input.Version, input.ContentVersion)
				status = http.StatusAccepted
			case "publish":
				out, err = service.Publish(ctx, token, key, rid, id, input.Version)
			case "withdraw":
				out, err = service.Withdraw(ctx, token, key, rid, id, input.Version)
			}
			if err != nil {
				adminError(c, err)
				return
			}
			wishMutationResult(c, status, out)
		}
		if op == "draft" {
			group.DELETE("/wishes/:id/draft", handler)
		} else {
			group.POST("/wishes/:id/"+op, handler)
		}
	}
	group.GET("/wishes/:id", func(c *gin.Context) {
		token := ""
		if len(c.Request.Header.Values("Authorization")) > 0 {
			var ok bool
			token, ok = bearer(c)
			if !ok {
				return
			}
		}
		out, err := service.Detail(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.GET("/me/wishes", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, err := wishQuery(c, true)
		if err != nil {
			adminError(c, err)
			return
		}
		out, err := service.Own(c.Request.Context(), token, c.GetString("request_id"), q)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.GET("/wishes", func(c *gin.Context) {
		q, err := wishQuery(c, false)
		if err != nil {
			adminError(c, err)
			return
		}
		out, err := service.List(c.Request.Context(), c.GetString("request_id"), q)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}

func wishMutationResult(c *gin.Context, status int, out wish.CreatedDraft) {
	adminResult(c, status, gin.H{"id": out.ID, "version": out.Version, "content_version": out.ContentVersion, "current_state": out.State, "allowed_actions": []string{}})
}
func wishQuery(c *gin.Context, own bool) (q wish.Query, err error) {
	q.Limit = 20
	allowed := []string{"cursor", "limit", "category", "campus"}
	if own {
		allowed = []string{"cursor", "limit", "state"}
	}
	for name, values := range c.Request.URL.Query() {
		if !slices.Contains(allowed, name) || len(values) != 1 || values[0] == "" {
			return q, command.ErrInvalid
		}
	}
	q.Cursor = c.Query("cursor")
	q.Category = c.Query("category")
	q.Campus = c.Query("campus")
	q.State = wish.State(c.Query("state"))
	if raw, ok := c.GetQuery("limit"); ok {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			return q, command.ErrInvalid
		}
		q.Limit = n
	}
	return q, nil
}
