package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// WishImageAPI 以独立授权边界上传与读取心愿图片。
type WishImageAPI interface {
	UploadImage(context.Context, string, string, string, string, io.Reader) (wish.Image, error)
	ReadImage(context.Context, string, string, string, string) ([]byte, error)
}

// WishIdentityCapability 将新客户端能力传入领域投影，旧客户端不获得半匿名作者结构。
func WishIdentityCapability() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(wish.WithIdentityCapability(c.Request.Context(), c.GetHeader("X-Wish-Identity-Version") == "2" || strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/")))
		c.Next()
	}
}

// RegisterWishImages 注册有界上传及逐次复核可见性的图片读取。
func RegisterWishImages(r *gin.Engine, service WishImageAPI) error {
	if r == nil || service == nil {
		return command.ErrInvalid
	}
	r.POST("/api/v1/wish-images", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, wish.ImageMaxBytes+(64<<10))
		reader, e := c.Request.MultipartReader()
		if e != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		part, e := reader.NextPart()
		if e != nil || part.FormName() != "file" || part.FileName() == "" {
			adminError(c, command.ErrInvalid)
			return
		}
		data, e := io.ReadAll(io.LimitReader(part, wish.ImageMaxBytes+1))
		defer clear(data)
		if e != nil || len(data) == 0 || len(data) > wish.ImageMaxBytes {
			adminError(c, command.ErrInvalid)
			return
		}
		media, parameters, e := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if e != nil || len(parameters) > 0 {
			adminError(c, command.ErrInvalid)
			return
		}
		if media == "application/octet-stream" {
			media = http.DetectContentType(data)
		}
		if media != "image/jpeg" && media != "image/png" {
			adminError(c, command.ErrInvalid)
			return
		}
		if _, e = reader.NextPart(); !errors.Is(e, io.EOF) {
			adminError(c, command.ErrInvalid)
			return
		}
		out, e := service.UploadImage(c.Request.Context(), token, key, c.GetString("request_id"), media, bytes.NewReader(data))
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	r.GET("/api/v1/wish-images/:id", func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		token, ok := optionalStoryToken(c)
		if !ok {
			return
		}
		data, e := service.ReadImage(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), c.Query("wish_id"))
		defer clear(data)
		if e != nil {
			adminError(c, e)
			return
		}
		c.Data(http.StatusOK, "image/png", data)
	})
	return nil
}

// RegisterAdminWishImages 由应用提供案件授权和图片读取协作，不允许对象ID单独授权。
func RegisterAdminWishImages(r *gin.Engine, read func(context.Context, string, string, string, string, string) ([]byte, error)) {
	r.GET("/api/v1/admin/wish-images/:id", func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		token, ok := bearer(c)
		if !ok {
			return
		}
		data, e := read(c.Request.Context(), token, c.GetString("request_id"), c.Query("case_id"), c.Query("wish_id"), c.Param("id"))
		defer clear(data)
		if e != nil {
			adminError(c, e)
			return
		}
		c.Data(http.StatusOK, "image/png", data)
	})
}
