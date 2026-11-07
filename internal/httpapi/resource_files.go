package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza"
)

type resourceFileUploader interface {
	UploadFile(context.Context, string, string, string, string, io.Reader) (*resourceplaza.FileInfo, error)
}

func registerResourceFiles(r *gin.Engine, service ResourcesAPI) {
	r.POST("/api/v1/admin/resource-files", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := bearer(c)
		if !ok {
			return
		}
		key, ok := idempotency(c)
		if !ok {
			return
		}
		uploader, ok := service.(resourceFileUploader)
		if !ok {
			resourceError(c, command.ErrUnavailable)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, resourceplaza.FileMaxBytes+(64<<10))
		reader, err := c.Request.MultipartReader()
		if err != nil {
			resourceError(c, command.ErrInvalid)
			return
		}
		part, err := reader.NextPart()
		if err != nil || part.FormName() != "file" || part.FileName() == "" {
			resourceError(c, command.ErrInvalid)
			return
		}
		name := part.FileName()
		data, err := io.ReadAll(io.LimitReader(part, resourceplaza.FileMaxBytes+1))
		defer clear(data)
		if len(data) > resourceplaza.FileMaxBytes {
			resourceHTTPError(c, 413, "PAYLOAD_TOO_LARGE")
			return
		}
		if err != nil {
			resourceError(c, command.ErrInvalid)
			return
		}
		if _, err = reader.NextPart(); !errors.Is(err, io.EOF) {
			resourceError(c, command.ErrInvalid)
			return
		}
		out, err := uploader.UploadFile(c.Request.Context(), token, key, c.GetString("request_id"), name, bytes.NewReader(data))
		if err != nil {
			resourceError(c, err)
			return
		}
		adminResult(c, http.StatusCreated, out)
	})
}
