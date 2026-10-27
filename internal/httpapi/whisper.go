package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperService 分离 HTTP 协议和领域；默认健康模式不装配此接口。
type WhisperService interface {
	Configuration(context.Context, string) (whisper.ModuleConfig, error)
	Help(context.Context, string) (whisper.HelpDirectory, error)
	Workspace(context.Context, string, string) (whisper.Workspace, error)
	CreatePost(context.Context, string, string, string, whisper.ContentInput) (whisper.OwnerPost, error)
	ChangePost(context.Context, string, string, string, string, string, whisper.PostInput) (whisper.OwnerPost, error)
	GetPost(context.Context, string, string, string, bool) (any, error)
	ListPosts(context.Context, string, string, bool, whisper.Query) (any, error)
	GetNickname(context.Context, string, string) (whisper.NicknameState, error)
	SetNickname(context.Context, string, string, string, whisper.NicknameInput) (whisper.NicknameState, error)
}

// RegisterWhispers 显式注册文字能力；审核、危机及隐私装配完成之前不得用于生产启用。
func RegisterWhispers(r *gin.Engine, s WhisperService) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	handle := func(op string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			c.Header("Pragma", "no-cache")
			ctx, rid := c.Request.Context(), c.GetString("request_id")
			token, e := whisperBearer(c, strings.HasPrefix(op, "own") || slicesWhisper(op, "workspace", "create", "update", "submit", "withdraw", "nickname_get", "nickname_set"))
			if e != nil {
				whisperError(c, e)
				return
			}
			var out any
			status := http.StatusOK
			if op != "public_list" && op != "own_list" && len(c.Request.URL.Query()) != 0 {
				whisperError(c, command.ErrInvalid)
				return
			}
			switch op {
			case "config":
				out, e = s.Configuration(ctx, rid)
			case "help":
				out, e = s.Help(ctx, rid)
			case "workspace":
				out, e = s.Workspace(ctx, token, rid)
			case "public_get", "own_get":
				id := c.Param("post_id")
				if !whisper.ValidID(id) {
					e = command.ErrInvalid
					break
				}
				out, e = s.GetPost(ctx, token, rid, id, op == "own_get")
			case "public_list", "own_list":
				var q whisper.Query
				q, e = whisperQuery(c)
				if e == nil {
					out, e = s.ListPosts(ctx, token, rid, op == "own_list", q)
				}
			case "nickname_get":
				out, e = s.GetNickname(ctx, token, rid)
			default:
				key := c.GetHeader("Idempotency-Key")
				if !whisper.ValidKey(key) || len(c.Request.Header.Values("Idempotency-Key")) != 1 {
					e = command.ErrInvalid
					break
				}
				var data []byte
				data, e = readWhisperJSON(c)
				if e != nil {
					break
				}
				if op == "nickname_set" {
					var input whisper.NicknameInput
					fields, failure := whisperObject(data, []string{"expected_version", "label"})
					if failure != nil {
						e = failure
						break
					}
					if json.Unmarshal(fields["expected_version"], &input.ExpectedVersion) != nil || json.Unmarshal(fields["label"], &input.Label) != nil || input.ExpectedVersion < 1 || !whisperText(input.Label, 2, 20) {
						e = command.ErrInvalid
						break
					}
					out, e = s.SetNickname(ctx, token, key, rid, input)
					status = http.StatusAccepted
					break
				}
				var input whisper.PostInput
				input, e = decodeWhisperPost(data, op)
				if e != nil {
					break
				}
				if op == "create" {
					out, e = s.CreatePost(ctx, token, key, rid, input.ContentInput)
					status = http.StatusCreated
				} else {
					id := c.Param("post_id")
					if !whisper.ValidID(id) {
						e = command.ErrInvalid
						break
					}
					out, e = s.ChangePost(ctx, token, key, rid, id, op, input)
					if op == "submit" {
						status = http.StatusAccepted
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
	for _, route := range []struct{ method, path, op string }{
		{"GET", "/whispers/config", "config"}, {"GET", "/whispers/help", "help"}, {"GET", "/me/whispers", "workspace"},
		{"GET", "/whispers/posts", "public_list"}, {"GET", "/whispers/posts/:post_id", "public_get"},
		{"GET", "/me/whispers/posts", "own_list"}, {"POST", "/me/whispers/posts", "create"}, {"GET", "/me/whispers/posts/:post_id", "own_get"},
		{"PUT", "/me/whispers/posts/:post_id", "update"}, {"POST", "/me/whispers/posts/:post_id/submit", "submit"}, {"POST", "/me/whispers/posts/:post_id/withdraw", "withdraw"},
		{"GET", "/me/whispers/nickname", "nickname_get"}, {"PUT", "/me/whispers/nickname", "nickname_set"},
	} {
		r.Handle(route.method, "/api/v1"+route.path, handle(route.op))
	}
	return nil
}

func slicesWhisper(value string, values ...string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func whisperBearer(c *gin.Context, required bool) (string, error) {
	values := c.Request.Header.Values("Authorization")
	if len(values) == 0 {
		if required {
			return "", command.ErrUnauthenticated
		}
		return "", nil
	}
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", command.ErrUnauthenticated
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", command.ErrUnauthenticated
	}
	return token, nil
}

func whisperQuery(c *gin.Context) (q whisper.Query, err error) {
	q.Limit = 20
	for k, values := range c.Request.URL.Query() {
		if len(values) != 1 {
			return q, command.ErrInvalid
		}
		switch k {
		case "limit":
			v, e := strconv.Atoi(values[0])
			if e != nil || strconv.Itoa(v) != values[0] || v < 1 || v > 50 {
				return q, command.ErrInvalid
			}
			q.Limit = v
		case "cursor":
			if len(values[0]) < 1 || len(values[0]) > 4096 {
				return q, command.ErrInvalid
			}
			q.Cursor = values[0]
		default:
			return q, command.ErrInvalid
		}
	}
	return
}

func readWhisperJSON(c *gin.Context) ([]byte, error) {
	media, _, e := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if e != nil || media != "application/json" {
		return nil, command.ErrInvalid
	}
	b, e := io.ReadAll(io.LimitReader(c.Request.Body, 16385))
	if e != nil || len(b) > 16384 || !strictWhisperUnicode(b) {
		return nil, command.ErrInvalid
	}
	return b, nil
}

// strictWhisperUnicode 在 encoding/json 替换非法代理项之前拒绝；转义反斜线不能冒充代理项。
func strictWhisperUnicode(b []byte) bool {
	if !utf8.Valid(b) || !json.Valid(b) {
		return false
	}
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		v, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return false
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func whisperObject(data []byte, keys []string) (map[string]json.RawMessage, error) {
	if !strictWhisperUnicode(data) {
		return nil, command.ErrInvalid
	}
	fields, e := wishObject(data, keys)
	if e != nil || len(fields) != len(keys) {
		return nil, command.ErrInvalid
	}
	return fields, nil
}

func whisperText(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && n >= min && n <= max && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, 0)
}

func decodeWhisperPost(data []byte, action string) (out whisper.PostInput, err error) {
	keys := []string{"expected_version"}
	if action == "create" {
		keys = []string{"body", "identity"}
	} else if action == "update" {
		keys = append(keys, "body", "identity")
	}
	fields, e := whisperObject(data, keys)
	if e != nil {
		return out, e
	}
	if action != "create" {
		if json.Unmarshal(fields["expected_version"], &out.ExpectedVersion) != nil || out.ExpectedVersion < 1 {
			return out, command.ErrInvalid
		}
	}
	if action == "submit" || action == "withdraw" {
		return out, nil
	}
	if json.Unmarshal(fields["body"], &out.Body) != nil || !whisperText(out.Body, 1, 500) {
		return out, command.ErrInvalid
	}
	identity, e := wishObject(fields["identity"], []string{"mode", "tags", "dictionary_version", "nickname_version"})
	if e != nil {
		return out, e
	}
	if json.Unmarshal(identity["mode"], &out.Identity.Mode) != nil {
		return out, command.ErrInvalid
	}
	switch out.Identity.Mode {
	case "anonymous":
		if len(identity) != 1 {
			return out, command.ErrInvalid
		}
	case "semi":
		if len(identity) != 3 || identity["tags"] == nil || identity["dictionary_version"] == nil {
			return out, command.ErrInvalid
		}
		tags, e := whisperObject(identity["tags"], []string{"campus", "grade", "gender"})
		if e != nil {
			return out, e
		}
		out.Identity.Tags = &whisper.Tags{}
		if json.Unmarshal(tags["campus"], &out.Identity.Tags.Campus) != nil || json.Unmarshal(tags["grade"], &out.Identity.Tags.Grade) != nil || json.Unmarshal(tags["gender"], &out.Identity.Tags.Gender) != nil || json.Unmarshal(identity["dictionary_version"], &out.Identity.DictionaryVersion) != nil || *out.Identity.DictionaryVersion < 1 {
			return out, command.ErrInvalid
		}
	case "named":
		if len(identity) != 2 || identity["nickname_version"] == nil || json.Unmarshal(identity["nickname_version"], &out.Identity.NicknameVersion) != nil || *out.Identity.NicknameVersion < 1 {
			return out, command.ErrInvalid
		}
	default:
		return out, command.ErrInvalid
	}
	return
}

func whisperError(c *gin.Context, err error) {
	status, code := 503, "DEPENDENCY_UNAVAILABLE"
	switch {
	case errors.Is(err, whisper.ErrModuleUnavailable):
		code = "MODULE_UNAVAILABLE"
	case errors.Is(err, command.ErrInvalid):
		status, code = 400, "VALIDATION_FAILED"
	case errors.Is(err, command.ErrUnauthenticated):
		status, code = 401, "UNAUTHENTICATED"
	case errors.Is(err, whisper.ErrVerificationRequired):
		status, code = 403, "VERIFICATION_REQUIRED"
	case errors.Is(err, command.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(err, command.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(err, command.ErrVersionConflict):
		status, code = 409, "VERSION_CONFLICT"
	case errors.Is(err, command.ErrIdempotencyConflict):
		status, code = 409, "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, whisper.ErrDictionaryChanged):
		status, code = 409, "DICTIONARY_CHANGED"
	case errors.Is(err, whisper.ErrModeUnavailable):
		status, code = 409, "MODE_UNAVAILABLE"
	case errors.Is(err, whisper.ErrNicknameNotReady):
		status, code = 409, "NICKNAME_NOT_READY"
	case errors.Is(err, command.ErrConflict):
		status, code = 409, "STATE_CONFLICT"
	case errors.Is(err, command.ErrInternal):
		status, code = 500, "INTERNAL_ERROR"
	}
	response := gin.H{"code": status, "message": "请求未完成", "error_code": code, "request_id": c.GetString("request_id")}
	var rate *whisper.RateError
	if errors.As(err, &rate) {
		status = 429
		response["code"] = status
		response["error_code"] = "RATE_LIMITED"
		seconds := max(1, rate.Seconds)
		response["retry_after_seconds"] = seconds
		c.Header("Retry-After", strconv.Itoa(seconds))
	}
	c.JSON(status, response)
}
