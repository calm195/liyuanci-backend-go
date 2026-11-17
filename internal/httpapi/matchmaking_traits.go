package httpapi

import (
	"context"
	"io"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
)

// MatchmakingTraitsAPI 单独声明词条接口，复用包分配的会话、写命令和错误协议。
type MatchmakingTraitsAPI interface {
	CreateTrait(context.Context, string, string, string, matchmaking.TraitCreateInput) (matchmaking.Receipt, error)
	UpdateTrait(context.Context, string, string, string, string, matchmaking.TraitUpdateInput) (matchmaking.Receipt, error)
	SuggestTraits(context.Context, string, string, string, string, string, int) (matchmaking.TraitCloud, error)
	ReadTraits(context.Context, string, string, matchmaking.TraitQuery) (matchmaking.TraitPage, error)
	ReadTraitCloud(context.Context, string, string, matchmaking.TraitQuery) (matchmaking.TraitCloud, error)
	ReadTrait(context.Context, string, string, string) (matchmaking.TraitDetail, error)
	ReviewTrait(context.Context, string, string, string, string, matchmaking.ReviewInput) (matchmaking.Receipt, error)
}

func registerMatchmakingTraits(r *gin.Engine, s MatchmakingTraitsAPI) {
	for _, route := range []struct{ method, path, op string }{
		{"GET", "/matchmaking/traits", "suggest"},
		{"GET", "/admin/matchmaking/traits", "list"},
		{"POST", "/admin/matchmaking/traits", "create"},
		{"PUT", "/admin/matchmaking/traits/:trait", "update"},
		{"GET", "/admin/matchmaking/traits/cloud", "cloud"},
		{"GET", "/admin/matchmaking/traits/:trait", "detail"},
		{"POST", "/admin/matchmaking/traits/:trait/review", "review"},
	} {
		r.Handle(route.method, "/api/v1"+route.path, func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			for _, h := range []string{"Authorization", "Content-Type", "Idempotency-Key"} {
				if len(c.Request.Header.Values(h)) > 1 {
					matchmakingError(c, command.ErrInvalid)
					return
				}
			}
			token, ok := bearer(c)
			if !ok {
				return
			}
			q, err := traitQuery(c.Request.URL.RawQuery, route.op)
			if err != nil {
				matchmakingError(c, err)
				return
			}
			ctx, rid := c.Request.Context(), c.GetString("request_id")
			var out any
			if route.op == "create" {
				var input matchmaking.TraitCreateInput
				key, ok := matchmakingInput(c, &input)
				if !ok {
					return
				}
				out, err = s.CreateTrait(ctx, token, key, rid, input)
			} else if route.op == "update" {
				var input matchmaking.TraitUpdateInput
				key, ok := matchmakingInput(c, &input)
				if !ok {
					return
				}
				out, err = s.UpdateTrait(ctx, token, key, rid, c.Param("trait"), input)
			} else if route.op == "review" {
				var input matchmaking.ReviewInput
				key, ok := matchmakingInput(c, &input)
				if !ok {
					return
				}
				out, err = s.ReviewTrait(ctx, token, key, rid, c.Param("trait"), input)
			} else {
				body, e := io.ReadAll(io.LimitReader(c.Request.Body, 1))
				if e != nil || len(body) > 0 {
					matchmakingError(c, command.ErrInvalid)
					return
				}
				switch route.op {
				case "suggest":
					out, err = s.SuggestTraits(ctx, token, rid, q.Q, q.Purpose, q.Category, q.Limit)
				case "list":
					out, err = s.ReadTraits(ctx, token, rid, q)
				case "cloud":
					out, err = s.ReadTraitCloud(ctx, token, rid, q)
				case "detail":
					out, err = s.ReadTrait(ctx, token, rid, c.Param("trait"))
				}
			}
			if err != nil {
				matchmakingError(c, err)
				return
			}
			adminResult(c, 200, out)
		})
	}
}

func traitQuery(raw, op string) (matchmaking.TraitQuery, error) {
	q := matchmaking.TraitQuery{Page: 1, Limit: 20}
	if op == "suggest" {
		q.Limit = 10
	}
	if op == "cloud" {
		q.Limit = 100
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return q, command.ErrInvalid
	}
	for key, list := range values {
		if len(list) != 1 || op == "detail" || op == "review" || op == "create" || op == "update" {
			return q, command.ErrInvalid
		}
		value := list[0]
		if op == "suggest" && key != "q" && key != "purpose" && key != "category" && key != "limit" {
			return q, command.ErrInvalid
		}
		switch key {
		case "category":
			q.Category = value
		case "enabled":
			if value != "true" && value != "false" {
				return q, command.ErrInvalid
			}
			enabled := value == "true"
			q.Enabled = &enabled
		case "purpose":
			q.Purpose = value
		case "q":
			q.Q = value
		case "gender":
			q.Gender = value
		case "status":
			q.Status = value
		case "sort":
			q.Sort = value
		case "page", "limit", "min_heat", "max_heat":
			if value == "" {
				return q, command.ErrInvalid
			}
			n, e := strconv.ParseInt(value, 10, 32)
			if e != nil || n < 0 {
				return q, command.ErrInvalid
			}
			switch key {
			case "page":
				if op == "cloud" || n < 1 {
					return q, command.ErrInvalid
				}
				q.Page = int(n)
			case "limit":
				if n < 1 {
					return q, command.ErrInvalid
				}
				q.Limit = int(n)
			case "min_heat":
				q.MinHeat = &n
			case "max_heat":
				q.MaxHeat = &n
			}
		default:
			return q, command.ErrInvalid
		}
	}
	return q, nil
}
