package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperCapsules 是五个本人胶囊操作的显式领域边界，不允许指定接收者。
type WhisperCapsules interface {
	CreateCapsule(context.Context, string, string, string, whisper.CapsuleInput) (whisper.Capsule, error)
	ListCapsules(context.Context, string, string, whisper.Query) (whisper.Page[whisper.Capsule], error)
	GetCapsule(context.Context, string, string, string) (whisper.Capsule, error)
	CloseCapsule(context.Context, string, string, string, string, string, int64) (whisper.Capsule, error)
}

// RegisterWhisperCapsules 不自动启用生产依赖或定时循环，所有返回均禁止缓存。
func RegisterWhisperCapsules(r *gin.Engine, s WhisperCapsules) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	handle := func(action string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			c.Header("Pragma", "no-cache")
			token, e := whisperBearer(c, true)
			if e != nil {
				whisperError(c, e)
				return
			}
			ctx, rid := c.Request.Context(), c.GetString("request_id")
			var out any
			status := http.StatusOK
			if action != "list" && len(c.Request.URL.Query()) > 0 {
				whisperError(c, command.ErrInvalid)
				return
			}
			switch action {
			case "list":
				var q whisper.Query
				q, e = whisperQuery(c)
				if e == nil {
					out, e = s.ListCapsules(ctx, token, rid, q)
				}
			case "get":
				out, e = s.GetCapsule(ctx, token, rid, c.Param("capsule_id"))
			default:
				key := c.GetHeader("Idempotency-Key")
				if !whisper.ValidKey(key) {
					whisperError(c, command.ErrInvalid)
					return
				}
				var b []byte
				b, e = readWhisperJSON(c)
				if e == nil {
					if action == "create" {
						var in whisper.CapsuleInput
						in, e = decodeWhisperCapsule(b)
						if e == nil {
							out, e = s.CreateCapsule(ctx, token, key, rid, in)
							status = http.StatusAccepted
						}
					} else {
						var fields map[string]json.RawMessage
						fields, e = whisperObject(b, []string{"expected_version"})
						var version int64
						if e == nil && (json.Unmarshal(fields["expected_version"], &version) != nil || version < 1) {
							e = command.ErrInvalid
						}
						if e == nil {
							out, e = s.CloseCapsule(ctx, token, key, rid, c.Param("capsule_id"), action, version)
						}
					}
				}
			}
			if e != nil {
				whisperError(c, e)
				return
			}
			adminResult(c, status, out)
		}
	}
	r.GET("/api/v1/me/whispers/capsules", handle("list"))
	r.POST("/api/v1/me/whispers/capsules", handle("create"))
	r.GET("/api/v1/me/whispers/capsules/:capsule_id", handle("get"))
	r.POST("/api/v1/me/whispers/capsules/:capsule_id/cancel", handle("cancel"))
	r.POST("/api/v1/me/whispers/capsules/:capsule_id/delete", handle("delete"))
	return nil
}

func decodeWhisperCapsule(data []byte) (out whisper.CapsuleInput, err error) {
	fields, e := whisperObject(data, []string{"body", "unlock_at", "safety_notice_accepted"})
	if e != nil {
		return out, e
	}
	var stamp string
	if json.Unmarshal(fields["body"], &out.Body) != nil || !whisperText(out.Body, 1, 500) || json.Unmarshal(fields["unlock_at"], &stamp) != nil || json.Unmarshal(fields["safety_notice_accepted"], &out.SafetyNoticeAccepted) != nil || !out.SafetyNoticeAccepted {
		return out, command.ErrInvalid
	}
	at, e := time.Parse(time.RFC3339, stamp)
	if e != nil || at.UTC().Format("2006-01-02T15:04:05Z") != stamp {
		return out, command.ErrInvalid
	}
	out.UnlockAt = at
	return out, nil
}
