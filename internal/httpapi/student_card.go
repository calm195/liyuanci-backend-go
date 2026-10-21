package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
)

// StudentCards 是显式注入的学生卡服务，不替代学生会话与人工认证。
type StudentCards interface {
	Upload(context.Context, string, string, string, string, io.Reader) (studentcard.Asset, error)
	Get(context.Context, string, string, string) (studentcard.Asset, error)
}

// RegisterStudentCards 只注册本人上传/状态路由；默认server不开放合成外部服务。
func RegisterStudentCards(r *gin.Engine, service StudentCards) error {
	if r == nil || service == nil {
		return command.ErrInvalid
	}
	group := r.Group("/api/v1/me/student-card-assets")
	group.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache"); c.Next() })
	group.POST("", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
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
