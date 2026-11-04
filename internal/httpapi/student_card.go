package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
)

const (
	studentCardUploadPath = "/api/v1/me/student-card-assets"
	// studentCardMultipartOverhead 为multipart边界和头部预留有界空间，不计入图片字节预算。
	studentCardMultipartOverhead int64 = 64 << 10
)

// StudentCards 是显式注入的学生卡服务，不替代学生会话与人工认证。
type StudentCards interface {
	Upload(context.Context, string, string, string, string, io.Reader) (studentcard.Asset, error)
	Get(context.Context, string, string, string) (studentcard.Asset, error)
}

// RegisterStudentCards 按显式文件字节预算注册本人上传/状态路由；默认server不开放合成外部服务。
func RegisterStudentCards(r *gin.Engine, service StudentCards, maxBytes int64) error {
	if r == nil || service == nil || maxBytes < 1 || maxBytes > 8<<20 {
		return command.ErrInvalid
	}
	group := r.Group(studentCardUploadPath)
	group.Use(func(c *gin.Context) {

		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
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
		// 原始文件大小仍由服务校验；总请求另含有界multipart开销，避免合法边界图片被提前截断。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+studentCardMultipartOverhead)
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
		defer part.Close()
		contentType, params, e := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if e != nil || len(params) != 0 {
			adminError(c, command.ErrInvalid)
			return
		}
		// 不调用ParseMultipartForm，避免学生卡明文被框架落到磁盘；只接受唯一file部分。
		// 服务须读至EOF再持久化；通过受限reader的EOF验证排除额外字段/文件。
		body := &singlePart{source: part, tail: func() error {
			// 不主动排空非法的额外部分；底层请求体仍由HTTP服务器关闭。
			_, e := reader.NextPart()
			if !errors.Is(e, io.EOF) {
				return command.ErrInvalid
			}
			return nil
		}}
		out, e := service.Upload(c.Request.Context(), token, key, c.GetString("request_id"), contentType, body)
		if e != nil {
			if studentCardImageError(c, e, maxBytes) {
				return
			}
			if errors.Is(e, studentcard.ErrBusy) {
				e = adminauth.ErrRateLimited
			}
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusAccepted, out)
	})
	group.GET("/:id", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		out, e := service.Get(c.Request.Context(), token, c.Param("id"), c.GetString("request_id"))
		if e != nil {
			if errors.Is(e, studentcard.ErrBusy) {
				e = adminauth.ErrRateLimited
			}
			adminError(c, e)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
	return nil
}

type singlePart struct {
	source io.Reader
	tail   func() error
	ended  bool
}

func (r *singlePart) Read(p []byte) (int, error) {
	if r.ended {
		return 0, io.EOF
	}
	n, e := r.source.Read(p)
	if errors.Is(e, io.EOF) {
		r.ended = true
		if failure := r.tail(); failure != nil {
			return n, failure
		}
	}
	return n, e
}

// studentCardImageError 返回明确的图片限制错误；不向客户端暴露解码器或内部错误原文。
func studentCardImageError(c *gin.Context, err error, maxBytes int64) bool {
	code := ""
	switch {
	case errors.Is(err, studentcard.ErrImageInputTooLarge):
		code = "STUDENT_CARD_IMAGE_TOO_LARGE"
	case errors.Is(err, studentcard.ErrImageNormalizedTooLarge):
		code = "STUDENT_CARD_NORMALIZED_IMAGE_TOO_LARGE"
	case errors.Is(err, studentcard.ErrImagePixelsExceeded):
		code = "STUDENT_CARD_PIXELS_EXCEEDED"
	default:
		return false
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"code":       http.StatusBadRequest,
		"message":    "图片不符合上传限制",
		"error_code": code,
		"request_id": c.GetString("request_id"),
		"max_bytes":  maxBytes,
	})
	return true
}
