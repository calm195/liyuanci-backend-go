package app

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func (m *wechatMedia) register(router *gin.Engine) {
	router.GET(mediaCallbackPath, m.challenge)
	router.POST(mediaCallbackPath, m.callback)
	router.GET(mediaSourcePath, m.serveSource)
	router.HEAD(mediaSourcePath, m.serveSource)
}

func (m *wechatMedia) challenge(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	echo, err := m.push.Challenge(c.Request.URL.Query())
	if err != nil {
		c.Status(http.StatusForbidden)
		return
	}
	c.String(http.StatusOK, "%s", echo)
}

func (m *wechatMedia) callback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	defer clear(raw)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	trace, decision, err := m.push.Result(c.Request.URL.Query(), raw)
	if err != nil {
		c.Status(http.StatusForbidden)
		return
	}
	if trace != "" {
		if err = m.record(c.Request.Context(), trace, decision); err != nil {
			m.log.Warn("微信图片审核结果写入失败", zap.String("request_id", c.GetString("request_id")))
			c.Status(http.StatusServiceUnavailable)
			return
		}
		m.log.Info("微信图片审核回调已处理", zap.String("request_id", c.GetString("request_id")))
	}
	c.String(http.StatusOK, "success")
}

func (m *wechatMedia) serveSource(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	data, err := m.source(c.Request.Context(), c.Query("id"))
	defer clear(data)
	if err != nil {
		if errors.Is(err, command.ErrNotFound) {
			c.Status(http.StatusNotFound)
		} else {
			c.Status(http.StatusServiceUnavailable)
		}
		return
	}
	c.Header("Content-Type", "image/png")
	c.Header("Content-Length", strconv.Itoa(len(data)))
	c.Header("X-Content-Type-Options", "nosniff")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, "image/png", data)
}
