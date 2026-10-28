package httpapi

import (
	"context"
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// InteractionBlockService 的兼容切换与树洞启用开关独立。
type InteractionBlockService interface {
	CreateBlock(context.Context, string, string, string, whisper.BlockInput) (whisper.BlockAck, error)
	ListBlocks(context.Context, string, string, whisper.Query) (whisper.Page[whisper.BlockEntry], error)
	DeleteBlock(context.Context, string, string, string, string, int64) error
}

// RegisterInteractionBlocks 显式注册匿名关系接口，必须与旧接口的升级屏障一起部署。
func RegisterInteractionBlocks(r *gin.Engine, s InteractionBlockService) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	handle := func(op string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			c.Header("Pragma", "no-cache")
			token, e := whisperBearer(c, true)
			if e != nil {
				whisperError(c, e)
				return
			}
			var out any
			if op == "list" {
				q, f := whisperQuery(c)
				e = f
				if e == nil {
					out, e = s.ListBlocks(c.Request.Context(), token, c.GetString("request_id"), q)
				}
			} else {
				if len(c.Request.URL.Query()) != 0 {
					whisperError(c, command.ErrInvalid)
					return
				}
				key := c.GetHeader("Idempotency-Key")
				if !whisper.ValidKey(key) || len(c.Request.Header.Values("Idempotency-Key")) != 1 {
					whisperError(c, command.ErrInvalid)
					return
				}
				raw, f := readWhisperJSON(c)
				if f != nil {
					whisperError(c, f)
					return
				}
				defer clear(raw)
				if op == "create" {
					fields, f := whisperObject(raw, []string{"source"})
					if f != nil {
						whisperError(c, f)
						return
					}
					var src map[string]json.RawMessage
					if json.Unmarshal(fields["source"], &src) != nil {
						whisperError(c, command.ErrInvalid)
						return
					}
					var kind string
					_ = json.Unmarshal(src["kind"], &kind)
					keys := []string{"kind", "id"}
					if kind != "public_profile" {
						keys = append(keys, "content_version")
					}
					if _, f = whisperObject(fields["source"], keys); f != nil {
						whisperError(c, f)
						return
					}
					var in whisper.BlockInput
					if json.Unmarshal(raw, &in) != nil {
						whisperError(c, command.ErrInvalid)
						return
					}
					out, e = s.CreateBlock(c.Request.Context(), token, key, c.GetString("request_id"), in)
				} else {
					fields, f := whisperObject(raw, []string{"expected_version"})
					if f != nil {
						whisperError(c, f)
						return
					}
					var version int64
					if json.Unmarshal(fields["expected_version"], &version) != nil {
						whisperError(c, command.ErrInvalid)
						return
					}
					e = s.DeleteBlock(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("block_id"), version)
				}
			}
			if e != nil {
				whisperError(c, e)
				return
			}
			if op == "delete" {
				c.Status(204)
				return
			}
			adminResult(c, 200, out)
		}
	}
	r.GET("/api/v1/me/interaction-blocks", handle("list"))
	r.POST("/api/v1/me/interaction-blocks", handle("create"))
	r.DELETE("/api/v1/me/interaction-blocks/:block_id", handle("delete"))
	return nil
}
