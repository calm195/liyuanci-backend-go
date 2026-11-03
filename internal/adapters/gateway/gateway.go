package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Client 拥有独立HTTP连接池；调用方在所有业务请求退出后调用Close。
type Client struct {
	client              *http.Client
	url, token, version string
	logger              *zap.Logger
}

// New 创建只访问显式HTTPS地址的客户端，拒绝重定向和无界请求期限。
func New(cfg Config, log *zap.Logger) (*Client, error) {
	u, e := url.Parse(cfg.URL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, command.ErrInvalid
	}
	credential := strings.TrimSpace(cfg.Token)
	if log == nil || cfg.Timeout <= 0 || len(credential) < 16 || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n\x00") {
		return nil, command.ErrInvalid
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CA != "" {
		pem := []byte(cfg.CA)
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, command.ErrInvalid
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{client, cfg.URL, credential, cfg.Version, log}, nil
}

// Close 释放本客户端的空闲连接，不拥有数据库或调用方上下文。
func (g *Client) Close() { g.client.CloseIdleConnections() }

func (g *Client) call(ctx context.Context, operation string, input, out any) (err error) {
	return g.callLimit(ctx, operation, input, out, 1<<20)
}

// callLimit 为私有加密对象响应提供独立上限，普通内容接口保持原有预算。
func (g *Client) callLimit(ctx context.Context, operation string, input, out any, limit int64) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		g.logger.Info(
			"业务供应商调用完成",
			zap.String("operation", operation),
			zap.String("result", result),
			zap.Duration("duration", time.Since(start)),
		)
	}()
	data, e := json.Marshal(struct {
		Operation, Version string
		Input              any
	}{operation, g.version, input})
	if e != nil {
		return command.ErrInvalid
	}
	defer clear(data)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(data))
	if e != nil {
		return command.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Content-Type", "application/json")
	response, e := g.client.Do(req)
	if e != nil {
		return command.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return command.ErrUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	defer clear(raw)
	if e != nil || int64(len(raw)) > limit {
		return command.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return command.ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return command.ErrUnavailable
	}
	return ctx.Err()
}
