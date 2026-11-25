package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/randomprofile"
)

// RandomProfilesAPI 提供授权抽取与素材维护，图片入口只读取已通过审核的展示字节。
type RandomProfilesAPI interface {
	ReadAdmin(context.Context, string, string) (randomprofile.Catalog, error)
	DrawAdmin(context.Context, string, string, randomprofile.DrawInput) (randomprofile.Draw, error)
	DrawStudent(context.Context, string, string, randomprofile.DrawInput) (randomprofile.Draw, error)
	Image(context.Context, string, string) ([]byte, error)
	CreateAvatar(context.Context, string, string, string, string, string, io.Reader) (randomprofile.Avatar, error)
	EditAvatar(context.Context, string, string, string, string, randomprofile.EditAvatarInput) (randomprofile.Avatar, error)
	ReplaceAvatarImage(context.Context, string, string, string, string, int64, string, io.Reader) (randomprofile.Avatar, error)
	CreateWord(context.Context, string, string, string, randomprofile.CreateWordInput) (randomprofile.Word, error)
	EditWord(context.Context, string, string, string, string, randomprofile.EditWordInput) (randomprofile.Word, error)
}

// RegisterRandomProfiles 注册后台配置、小程序试抽和素材下载路由。
func RegisterRandomProfiles(r *gin.Engine, s RandomProfilesAPI) error {
	if r == nil || s == nil {
		return command.ErrInvalid
	}
	group := r.Group("/api/v1")
	group.GET("/random-profiles/images/:id", func(c *gin.Context) {
		data, err := s.Image(c.Request.Context(), c.Param("id"), c.GetString("request_id"))
		if err != nil {
			c.Header("Cache-Control", "no-store")
			adminError(c, err)
			return
		}
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "image/png", data)
	})
	private := group.Group("", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if len(c.Request.URL.Query()) != 0 {
			returnInvalid(c)
			return
		}
		c.Next()
	})
	private.POST("/me/random-profile", randomProfileDraw(s.DrawStudent))
	admin := private.Group("/admin/random-profiles")
	admin.POST("/draw", randomProfileDraw(s.DrawAdmin))
	admin.GET("", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, err := s.ReadAdmin(c.Request.Context(), token, c.GetString("request_id"))
		randomProfileResult(c, out, err)
	})
	admin.POST("/avatars", randomAvatarUpload("name", 1024, func(c *gin.Context, token, key, value, media string, src io.Reader) (randomprofile.Avatar, error) {
		return s.CreateAvatar(c.Request.Context(), token, key, c.GetString("request_id"), value, media, src)
	}))
	admin.POST("/avatars/:id/image", randomAvatarUpload("expected_version", 19, func(c *gin.Context, token, key, value, media string, src io.Reader) (randomprofile.Avatar, error) {
		version, err := strconv.ParseInt(value, 10, 64)
		if err != nil || version < 1 {
			return randomprofile.Avatar{}, command.ErrInvalid
		}
		return s.ReplaceAvatarImage(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), version, media, src)
	}))
	admin.PUT("/avatars/:id", randomProfileWrite(func(c *gin.Context, token, key string, input randomprofile.EditAvatarInput) (randomprofile.Avatar, error) {
		return s.EditAvatar(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), input)
	}))
	admin.POST("/words", randomProfileWrite(func(c *gin.Context, token, key string, input randomprofile.CreateWordInput) (randomprofile.Word, error) {
		return s.CreateWord(c.Request.Context(), token, key, c.GetString("request_id"), input)
	}))
	admin.PUT("/words/:id", randomProfileWrite(func(c *gin.Context, token, key string, input randomprofile.EditWordInput) (randomprofile.Word, error) {
		return s.EditWord(c.Request.Context(), token, key, c.GetString("request_id"), c.Param("id"), input)
	}))
	return nil
}

func randomProfileDraw(draw func(context.Context, string, string, randomprofile.DrawInput) (randomprofile.Draw, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var input randomprofile.DrawInput
		if strictJSON(c, &input) != nil {
			returnInvalid(c)
			return
		}
		out, err := draw(c.Request.Context(), token, c.GetString("request_id"), input)
		randomProfileResult(c, out, err)
	}
}

func randomProfileWrite[I, O any](execute func(*gin.Context, string, string, I) (O, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var input I
		if strictJSON(c, &input) != nil {
			returnInvalid(c)
			return
		}
		out, err := execute(c, token, key, input)
		randomProfileResult(c, out, err)
	}
}

func randomAvatarUpload(field string, limit int64, execute func(*gin.Context, string, string, string, string, io.Reader) (randomprofile.Avatar, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		value, media, data, err := readAvatarMultipart(c, field, limit)
		defer clear(data)
		if err != nil {
			adminError(c, err)
			return
		}
		out, err := execute(c, token, key, value, media, bytes.NewReader(data))
		randomProfileResult(c, out, err)
	}
}

func randomProfileResult(c *gin.Context, data any, err error) {
	status, code := 0, ""
	switch {
	case err == nil:
		adminResult(c, http.StatusOK, data)
		return
	case errors.Is(err, randomprofile.ErrPoolEmpty):
		status, code = http.StatusConflict, "RANDOM_PROFILE_POOL_EMPTY"
	case errors.Is(err, randomprofile.ErrNicknameTooLong):
		status, code = http.StatusConflict, "RANDOM_PROFILE_NICKNAME_TOO_LONG"
	case errors.Is(err, randomprofile.ErrDuplicateWord):
		status, code = http.StatusConflict, "RANDOM_PROFILE_DUPLICATE_WORD"
	case errors.Is(err, randomprofile.ErrImageRejected):
		status, code = http.StatusBadRequest, "RANDOM_PROFILE_IMAGE_REJECTED"
	case errors.Is(err, randomprofile.ErrImageBusy):
		status, code = http.StatusTooManyRequests, "RATE_LIMITED"
	default:
		adminError(c, err)
		return
	}
	c.AbortWithStatusJSON(status, gin.H{
		"code": status, "message": err.Error(), "error_code": code, "request_id": c.GetString("request_id"),
	})
}
