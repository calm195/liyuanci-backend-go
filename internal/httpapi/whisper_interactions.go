package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperInteractions 是暖评、抱抱和本人通知的显式业务边界。
type WhisperInteractions interface {
	SaveComment(context.Context, string, string, string, string, string, whisper.CommentInput) (whisper.OwnerComment, error)
	WithdrawComment(context.Context, string, string, string, string, int64) (whisper.OwnerComment, error)
	SetComments(context.Context, string, string, string, string, int64, bool) (whisper.OwnerPost, error)
	GetComment(context.Context, string, string, string) (whisper.OwnerComment, error)
	ListComments(context.Context, string, string, string, bool, whisper.Query) (any, error)
	SetHug(context.Context, string, string, string, string, whisper.HugInput) (whisper.HugResult, error)
	Notifications(context.Context, string, string, whisper.Query) (whisper.Page[whisper.Notification], error)
	ReadNotification(context.Context, string, string, string, string, int64) (whisper.Notification, error)
}

// RegisterWhisperInteractions 注册十个互动操作；不改变健康模式或自动装配生产依赖。
func RegisterWhisperInteractions(r *gin.Engine, s WhisperInteractions) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	handle := func(op string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			c.Header("Pragma", "no-cache")
			token, e := whisperBearer(c, op != "public_comments")
			if e != nil {
				whisperError(c, e)
				return
			}
			ctx, rid := c.Request.Context(), c.GetString("request_id")
			var out any
			status := http.StatusOK
			list := slicesWhisper(op, "public_comments", "own_comments", "notifications")
			if !list && len(c.Request.URL.Query()) > 0 {
				whisperError(c, command.ErrInvalid)
				return
			}
			if list {
				q, err := whisperQuery(c)
				e = err
				if e == nil {
					if op == "notifications" {
						out, e = s.Notifications(ctx, token, rid, q)
					} else {
						out, e = s.ListComments(ctx, token, rid, c.Param("post_id"), op == "own_comments", q)
					}
				}
			} else if op == "comment_get" {
				if !whisper.ValidID(c.Param("comment_id")) {
					e = command.ErrInvalid
				} else {
					out, e = s.GetComment(ctx, token, rid, c.Param("comment_id"))
				}
			} else {
				key := c.GetHeader("Idempotency-Key")
				if !whisper.ValidKey(key) {
					whisperError(c, command.ErrInvalid)
					return
				}
				data, err := readWhisperJSON(c)
				e = err
				if e == nil {
					switch op {
					case "create", "update":
						var in whisper.CommentInput
						in, e = decodeWhisperComment(data, op == "update")
						if e == nil {
							out, e = s.SaveComment(ctx, token, key, rid, c.Param("post_id"), c.Param("comment_id"), in)
							status = http.StatusAccepted
						}
					case "withdraw", "read":
						var fields map[string]json.RawMessage
						fields, e = whisperObject(data, []string{"expected_version"})
						var version int64
						if e == nil && (json.Unmarshal(fields["expected_version"], &version) != nil || version < 1) {
							e = command.ErrInvalid
						}
						if e == nil {
							if op == "withdraw" {
								out, e = s.WithdrawComment(ctx, token, key, rid, c.Param("comment_id"), version)
							} else {
								out, e = s.ReadNotification(ctx, token, key, rid, c.Param("notification_id"), version)
							}
						}
					case "setting":
						var fields map[string]json.RawMessage
						fields, e = whisperObject(data, []string{"expected_version", "comments_open"})
						var version int64
						var open bool
						if e == nil && (json.Unmarshal(fields["expected_version"], &version) != nil || version < 1 || json.Unmarshal(fields["comments_open"], &open) != nil) {
							e = command.ErrInvalid
						}
						if e == nil {
							out, e = s.SetComments(ctx, token, key, rid, c.Param("post_id"), version, open)
						}
					case "hug":
						_, e = whisperObject(data, []string{"expected_post_version", "expected_hug_version", "active"})
						var in whisper.HugInput
						if e == nil && (json.Unmarshal(data, &in) != nil || in.ExpectedPostVersion < 1 || in.ExpectedHugVersion < 0) {
							e = command.ErrInvalid
						}
						if e == nil {
							out, e = s.SetHug(ctx, token, key, rid, c.Param("post_id"), in)
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
	for _, v := range []struct{ method, path, op string }{
		{"GET", "/whispers/posts/:post_id/comments", "public_comments"}, {"POST", "/whispers/posts/:post_id/comments", "create"},
		{"PUT", "/me/whispers/posts/:post_id/comments-setting", "setting"}, {"GET", "/me/whispers/comments", "own_comments"},
		{"GET", "/me/whispers/comments/:comment_id", "comment_get"}, {"PUT", "/me/whispers/comments/:comment_id", "update"}, {"POST", "/me/whispers/comments/:comment_id/withdraw", "withdraw"},
		{"PUT", "/whispers/posts/:post_id/hugs", "hug"}, {"GET", "/me/whispers/notifications", "notifications"}, {"POST", "/me/whispers/notifications/:notification_id/read", "read"},
	} {
		r.Handle(v.method, "/api/v1"+v.path, handle(v.op))
	}
	return nil
}

func decodeWhisperComment(data []byte, update bool) (out whisper.CommentInput, err error) {
	keys := []string{"expected_post_version", "expected_comments_epoch", "body", "identity"}
	if update {
		keys = append(keys, "expected_version")
	}
	fields, e := whisperObject(data, keys)
	if e != nil {
		return out, e
	}
	if json.Unmarshal(fields["expected_post_version"], &out.ExpectedPostVersion) != nil || out.ExpectedPostVersion < 1 || json.Unmarshal(fields["expected_comments_epoch"], &out.ExpectedCommentsEpoch) != nil || out.ExpectedCommentsEpoch < 1 {
		return out, command.ErrInvalid
	}
	if update && (json.Unmarshal(fields["expected_version"], &out.ExpectedVersion) != nil || out.ExpectedVersion < 1) {
		return out, command.ErrInvalid
	}
	b, _ := json.Marshal(map[string]json.RawMessage{"body": fields["body"], "identity": fields["identity"]})
	post, e := decodeWhisperPost(b, "create")
	out.ContentInput = post.ContentInput
	return out, e
}
