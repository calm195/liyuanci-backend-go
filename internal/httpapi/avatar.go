package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
)

// AvatarAPI 只允许更新当前学生会话的头像。
type AvatarAPI interface {
	UploadAvatar(context.Context, string, string, string, int64, string, io.Reader) (studentauth.Me, error)
	DeleteAvatar(context.Context, string, string, string, int64) (studentauth.Me, error)
}

// RegisterAvatar 注册本人头像上传和恢复默认入口。
func RegisterAvatar(r *gin.Engine, service AvatarAPI) error {
	if r == nil || service == nil {
		return command.ErrInvalid
	}
	group := r.Group("/api/v1/me/avatar", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	group.POST("", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		version, mediaType, data, err := readAvatarUpload(c)
		defer clear(data)
		if err != nil {
			adminError(c, err)
			return
		}
		out, err := service.UploadAvatar(c.Request.Context(), token, key, c.GetString("request_id"), version, mediaType, bytes.NewReader(data))
		if err != nil {
			if errors.Is(err, studentauth.ErrRateLimited) {
				err = adminauth.ErrRateLimited
			}
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	group.DELETE("", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		var input struct {
			ExpectedVersion int64 `json:"expected_version"`
		}
		if strictJSON(c, &input) != nil || input.ExpectedVersion < 1 {
			adminError(c, command.ErrInvalid)
			return
		}
		out, err := service.DeleteAvatar(c.Request.Context(), token, key, c.GetString("request_id"), input.ExpectedVersion)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}

func readAvatarUpload(c *gin.Context) (version int64, mediaType string, data []byte, err error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, studentauth.AvatarMaxBytes+(64<<10))
	reader, e := c.Request.MultipartReader()
	if e != nil {
		return 0, "", nil, command.ErrInvalid
	}
	// 两个字段允许任意顺序，不使用会将明文落盘的框架表单解析。
	for range 2 {
		part, e := reader.NextPart()
		if e != nil {
			return version, mediaType, data, command.ErrInvalid
		}
		switch part.FormName() {
		case "file":
			if data != nil || part.FileName() == "" {
				return version, mediaType, data, command.ErrInvalid
			}
			var parameters map[string]string
			declared := part.Header.Get("Content-Type")
			if declared == "" {
				declared = "application/octet-stream"
			}
			mediaType, parameters, e = mime.ParseMediaType(declared)
			if e != nil || len(parameters) != 0 || mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "application/octet-stream" {
				return version, mediaType, data, command.ErrInvalid
			}
			data, e = io.ReadAll(io.LimitReader(part, studentauth.AvatarMaxBytes+1))
			if e != nil || len(data) == 0 || len(data) > studentauth.AvatarMaxBytes {
				return version, mediaType, data, command.ErrInvalid
			}
			if mediaType == "application/octet-stream" {
				mediaType = http.DetectContentType(data)
				if mediaType != "image/png" && mediaType != "image/jpeg" {
					return version, mediaType, data, command.ErrInvalid
				}
			}
		case "expected_version":
			if version != 0 || part.FileName() != "" {
				return version, mediaType, data, command.ErrInvalid
			}
			raw, e := io.ReadAll(io.LimitReader(part, 20))
			if e != nil || len(raw) == 20 {
				return version, mediaType, data, command.ErrInvalid
			}
			version, e = strconv.ParseInt(string(raw), 10, 64)
			if e != nil || version < 1 {
				return version, mediaType, data, command.ErrInvalid
			}
		default:
			return version, mediaType, data, command.ErrInvalid
		}
	}
	if _, e := reader.NextPart(); !errors.Is(e, io.EOF) || version < 1 || len(data) == 0 {
		return version, mediaType, data, command.ErrInvalid
	}
	return version, mediaType, data, nil
}
