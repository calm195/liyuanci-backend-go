package httpapi

import (
	"context"
	"io"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
)

// MatchmakingAutoAPI 是持久自动分配的生成、读取和确认边界。
type MatchmakingAutoAPI interface {
	CreateAutoJob(context.Context, string, string, string, string, matchmaking.VersionInput) (matchmaking.Receipt, error)
	ReadAutoJob(context.Context, string, string, string, string) (matchmaking.AutoJob, error)
	ApplyAutoJob(context.Context, string, string, string, string, string, matchmaking.AutoApplyInput) (matchmaking.Receipt, error)
}

func registerMatchmakingAuto(r *gin.Engine, s MatchmakingAutoAPI) {
	for _, rt := range []struct{ method, path, op string }{
		{"POST", "/admin/matchmaking/activities/:id/auto-pairing-jobs", "create"},
		{"GET", "/admin/matchmaking/activities/:id/auto-pairing-jobs/:job", "read"},
		{"POST", "/admin/matchmaking/activities/:id/auto-pairing-jobs/:job/apply", "apply"},
	} {
		r.Handle(rt.method, "/api/v1"+rt.path, func(c *gin.Context) {
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
			if c.Request.URL.RawQuery != "" {
				matchmakingError(c, command.ErrInvalid)
				return
			}
			var out any
			var err error
			ctx, rid, id := c.Request.Context(), c.GetString("request_id"), c.Param("id")
			switch rt.op {
			case "read":
				body, e := io.ReadAll(io.LimitReader(c.Request.Body, 1))
				if e != nil || len(body) > 0 {
					matchmakingError(c, command.ErrInvalid)
					return
				}
				out, err = s.ReadAutoJob(ctx, token, rid, id, c.Param("job"))
			case "create":
				var input matchmaking.VersionInput
				key, ok := matchmakingInput(c, &input)
				if !ok {
					return
				}
				out, err = s.CreateAutoJob(ctx, token, key, rid, id, input)
			case "apply":
				var input matchmaking.AutoApplyInput
				key, ok := matchmakingInput(c, &input)
				if !ok {
					return
				}
				out, err = s.ApplyAutoJob(ctx, token, key, rid, id, c.Param("job"), input)
			}
			if err != nil {
				matchmakingError(c, err)
				return
			}
			adminResult(c, 200, out)
		})
	}
}
