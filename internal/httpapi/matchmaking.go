package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
)

// MatchmakingAPI 显式列出活动、报名、配对与任务命令，所有写入返回技术回执。
type MatchmakingAPI interface {
	ReadLibrary(context.Context, string, string, int, int) (matchmaking.LibraryPage, error)
	SaveLibraryTask(context.Context, string, string, string, string, matchmaking.LibraryTaskInput) (matchmaking.Receipt, error)
	SaveTaskPlan(context.Context, string, string, string, string, matchmaking.TaskPlanInput) (matchmaking.Receipt, error)
	ReadProfile(context.Context, string, string) (matchmaking.AccountProfile, error)
	SaveProfile(context.Context, string, string, string, matchmaking.ProfileInput) (matchmaking.Receipt, error)
	List(context.Context, string, string, bool, int, int) (matchmaking.Page, error)
	Read(context.Context, string, string, string) (matchmaking.Detail, error)
	ReadAdmin(context.Context, string, string, string, int, int) (matchmaking.Workspace, error)
	Candidates(context.Context, string, string, string, string) ([]matchmaking.Candidate, error)
	SaveActivity(context.Context, string, string, string, string, matchmaking.ActivityInput) (matchmaking.Receipt, error)
	ActivityCommand(context.Context, string, string, string, string, string, matchmaking.VersionInput) (matchmaking.Receipt, error)
	SaveTemplates(context.Context, string, string, string, string, matchmaking.TemplatesInput) (matchmaking.Receipt, error)
	Enroll(context.Context, string, string, string, string, matchmaking.EnrollmentInput) (matchmaking.Receipt, error)
	Review(context.Context, string, string, string, string, string, matchmaking.ReviewInput) (matchmaking.Receipt, error)
	Withdraw(context.Context, string, string, string, string, matchmaking.VersionInput) (matchmaking.Receipt, error)
	OpenTask(context.Context, string, string, string, string, string) (matchmaking.Receipt, error)
	SetPair(context.Context, string, string, string, string, matchmaking.PairInput) (matchmaking.Receipt, error)
	EndPair(context.Context, string, string, string, string, string, matchmaking.VersionInput) (matchmaking.Receipt, error)
}

// RegisterMatchmaking 按岗位与学生会话分别进入同一事务领域服务。
func RegisterMatchmaking(r *gin.Engine, s MatchmakingAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	for _, rt := range []struct {
		method, path, op string
		admin            bool
	}{
		{"GET", "/matchmaking/profile", "profile", false},
		{"PUT", "/matchmaking/profile", "profile_save", false},
		{"GET", "/matchmaking/activities", "list", false},
		{"GET", "/matchmaking/activities/:id", "detail", false},
		{"PUT", "/matchmaking/activities/:id/enrollment", "enroll", false},
		{"POST", "/matchmaking/activities/:id/withdraw", "withdraw", false},
		{"POST", "/matchmaking/activities/:id/tasks/:task/open", "open", false},
		{"GET", "/admin/matchmaking/task-library", "library", true},
		{"POST", "/admin/matchmaking/task-library", "library_save", true},
		{"PUT", "/admin/matchmaking/task-library/:task", "library_save", true},
		{"PUT", "/admin/matchmaking/activities/:id/task-plan", "task_plan", true},
		{"GET", "/admin/matchmaking/activities", "list", true},
		{"POST", "/admin/matchmaking/activities", "save", true},
		{"GET", "/admin/matchmaking/activities/:id", "workspace", true},
		{"PUT", "/admin/matchmaking/activities/:id", "save", true},
		{"POST", "/admin/matchmaking/activities/:id/publish", "publish", true},
		{"POST", "/admin/matchmaking/activities/:id/cancel", "cancel", true},
		{"POST", "/admin/matchmaking/activities/:id/results/publish", "results/publish", true},
		{"PUT", "/admin/matchmaking/activities/:id/templates", "templates", true},
		{"POST", "/admin/matchmaking/activities/:id/enrollments/:enrollment/review", "review", true},
		{"GET", "/admin/matchmaking/activities/:id/candidates/:enrollment", "candidates", true},
		{"POST", "/admin/matchmaking/activities/:id/pairs", "pair", true},
		{"POST", "/admin/matchmaking/activities/:id/pairs/:pair/end", "end", true},
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
			page, limit, err := matchmakingQuery(c.Request.URL.RawQuery, rt.op == "list" || rt.op == "workspace" || rt.op == "library")
			if err != nil {
				matchmakingError(c, err)
				return
			}
			ctx, rid, id := c.Request.Context(), c.GetString("request_id"), c.Param("id")
			var out any
			if rt.method == "GET" {
				body, e := io.ReadAll(io.LimitReader(c.Request.Body, 1))
				if e != nil || len(body) > 0 {
					matchmakingError(c, command.ErrInvalid)
					return
				}
				switch rt.op {
				case "library":
					out, err = s.ReadLibrary(ctx, token, rid, page, limit)
				case "list":
					out, err = s.List(ctx, token, rid, rt.admin, page, limit)
				case "profile":
					out, err = s.ReadProfile(ctx, token, rid)
				case "detail":
					out, err = s.Read(ctx, token, rid, id)
				case "workspace":
					out, err = s.ReadAdmin(ctx, token, rid, id, page, limit)
				case "candidates":
					var items []matchmaking.Candidate
					items, err = s.Candidates(ctx, token, rid, id, c.Param("enrollment"))
					out = gin.H{"items": items}
				}
			} else {
				var input any
				switch rt.op {
				case "save":
					input = &matchmaking.ActivityInput{}
				case "profile_save":
					input = &matchmaking.ProfileInput{}
				case "enroll":
					input = &matchmaking.EnrollmentInput{}
				case "review":
					input = &matchmaking.ReviewInput{}
				case "library_save":
					input = &matchmaking.LibraryTaskInput{}
				case "task_plan":
					input = &matchmaking.TaskPlanInput{}
				case "templates":
					input = &matchmaking.TemplatesInput{}
				case "pair":
					input = &matchmaking.PairInput{}
				case "open":
					input = &struct{}{}
				default:
					input = &matchmaking.VersionInput{}
				}
				key, ok := matchmakingInput(c, input)
				if !ok {
					return
				}
				switch rt.op {
				case "save":
					out, err = s.SaveActivity(ctx, token, key, rid, id, *input.(*matchmaking.ActivityInput))
				case "profile_save":
					out, err = s.SaveProfile(ctx, token, key, rid, *input.(*matchmaking.ProfileInput))
				case "enroll":
					out, err = s.Enroll(ctx, token, key, rid, id, *input.(*matchmaking.EnrollmentInput))
				case "review":
					out, err = s.Review(ctx, token, key, rid, id, c.Param("enrollment"), *input.(*matchmaking.ReviewInput))
				case "library_save":
					out, err = s.SaveLibraryTask(ctx, token, key, rid, c.Param("task"), *input.(*matchmaking.LibraryTaskInput))
				case "task_plan":
					out, err = s.SaveTaskPlan(ctx, token, key, rid, id, *input.(*matchmaking.TaskPlanInput))
				case "templates":
					out, err = s.SaveTemplates(ctx, token, key, rid, id, *input.(*matchmaking.TemplatesInput))
				case "pair":
					out, err = s.SetPair(ctx, token, key, rid, id, *input.(*matchmaking.PairInput))
				case "end":
					out, err = s.EndPair(ctx, token, key, rid, id, c.Param("pair"), *input.(*matchmaking.VersionInput))
				case "withdraw":
					out, err = s.Withdraw(ctx, token, key, rid, id, *input.(*matchmaking.VersionInput))
				case "open":
					out, err = s.OpenTask(ctx, token, key, rid, id, c.Param("task"))
				default:
					out, err = s.ActivityCommand(ctx, token, key, rid, id, rt.op, *input.(*matchmaking.VersionInput))
				}
			}
			if err != nil {
				matchmakingError(c, err)
				return
			}
			adminResult(c, 200, out)
		})
	}
	if auto, ok := s.(MatchmakingAutoAPI); ok {
		registerMatchmakingAuto(r, auto)
	}
	if traits, ok := s.(MatchmakingTraitsAPI); ok {
		registerMatchmakingTraits(r, traits)
	}
	return nil
}

func matchmakingQuery(raw string, paged bool) (page, limit int, err error) {
	page, limit = 1, 20
	q, e := url.ParseQuery(raw)
	if e != nil {
		return 0, 0, command.ErrInvalid
	}
	for key, values := range q {
		if !paged || len(values) != 1 || (key != "page" && key != "limit") {
			return 0, 0, command.ErrInvalid
		}
		n, e := strconv.Atoi(values[0])
		if e != nil || n < 1 {
			return 0, 0, command.ErrInvalid
		}
		if key == "page" {
			page = n
		} else {
			limit = n
		}
	}
	if page > 10000 || limit > 50 {
		return 0, 0, command.ErrInvalid
	}
	return
}

// matchmakingInput 的独立128KiB预算容纳30项1000字模板；未知及嵌套重复字段均拒绝。
func matchmakingInput(c *gin.Context, out any) (string, bool) {
	media, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || media != "application/json" || len(params) > 1 || len(params) == 1 && strings.ToLower(params["charset"]) != "utf-8" {
		matchmakingHTTPError(c, 415, "UNSUPPORTED_MEDIA_TYPE")
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 131073))
	if len(raw) > 131072 {
		matchmakingHTTPError(c, 413, "PAYLOAD_TOO_LARGE")
		return "", false
	}
	key := c.GetHeader("Idempotency-Key")
	if err != nil || len(key) < 1 || len(key) > 128 {
		matchmakingError(c, command.ErrInvalid)
		return "", false
	}
	for _, v := range key {
		if v < 33 || v > 126 {
			matchmakingError(c, command.ErrInvalid)
			return "", false
		}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || matchmakingJSONValue(json.NewDecoder(bytes.NewReader(raw))) != nil {
		matchmakingError(c, command.ErrInvalid)
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		matchmakingError(c, command.ErrInvalid)
		return "", false
	}
	if _, err = decoder.Token(); err != io.EOF {
		matchmakingError(c, command.ErrInvalid)
		return "", false
	}
	return key, true
}

func matchmakingJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return command.ErrInvalid
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || name != strings.ToLower(name) || seen[name] {
				return command.ErrInvalid
			}
			seen[name] = true
		}
		if err := matchmakingJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func matchmakingHTTPError(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"code": status, "message": "包分配请求未完成", "error_code": code, "request_id": c.GetString("request_id")})
}

func matchmakingError(c *gin.Context, err error) {
	status, code := 503, "DEPENDENCY_UNAVAILABLE"
	for _, v := range []struct {
		err    error
		status int
		code   string
	}{
		{command.ErrInvalid, 400, "VALIDATION_FAILED"}, {command.ErrUnauthenticated, 401, "UNAUTHENTICATED"},
		{command.ErrForbidden, 403, "FORBIDDEN"}, {command.ErrVerificationRequired, 403, "VERIFICATION_REQUIRED"},
		{command.ErrNotFound, 404, "NOT_FOUND"}, {command.ErrVersionConflict, 409, "VERSION_CONFLICT"},
		{command.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"}, {command.ErrConflict, 409, "STATE_CONFLICT"},
		{command.ErrReviewPending, 409, "REVIEW_PENDING"}, {command.ErrBlockedInteraction, 409, "BLOCKED_INTERACTION"},
		{command.ErrRetry, 409, "RETRY_REQUIRED"}, {command.ErrInternal, 500, "INTERNAL_ERROR"},
	} {
		if errors.Is(err, v.err) {
			status, code = v.status, v.code
			break
		}
	}
	matchmakingHTTPError(c, status, code)
}
