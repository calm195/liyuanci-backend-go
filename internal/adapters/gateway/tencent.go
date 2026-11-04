// Package gateway 实现正式微信及腾讯云HTTPS协议；不读取应用配置，不装配业务服务。
package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// TencentConfig 只包含腾讯云原生协议参数；凭据由私有YAML显式注入。
type TencentConfig struct {
	Bucket, Region, SecretID, SecretKey, SessionToken, BizType string
	ObjectURL, ImageURL, CA                                    string
	Timeout                                                    time.Duration
	MaxObjectBytes                                             int64
}

// Tencent 提供不可覆盖密文对象与同步图片审核，连接池由装配层统一关闭。
type Tencent struct {
	client          *http.Client
	transport       *http.Transport
	objects, images string
	bizType         string
	maxObjectBytes  int64
	logger          *zap.Logger
}

// NewTencent 默认使用腾讯云HTTPS域名，专用代理端点同样要求HTTPS并拒绝重定向。
func NewTencent(cfg TencentConfig, logger *zap.Logger) (*Tencent, error) {
	if logger == nil || cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second || cfg.MaxObjectBytes <= 0 ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,49}-[0-9]{5,20}$`).MatchString(cfg.Bucket) ||
		!regexp.MustCompile(`^[a-z]{2}-[a-z]+(?:-[a-z]+)?$`).MatchString(cfg.Region) ||
		!validTencentSecret(cfg.SecretID) || !validTencentSecret(cfg.SecretKey) ||
		cfg.SessionToken != "" && !validTencentSecret(cfg.SessionToken) ||
		!regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`).MatchString(cfg.BizType) {
		return nil, command.ErrInvalid
	}
	objects := "https://" + cfg.Bucket + ".cos." + cfg.Region + ".myqcloud.com"
	images := "https://" + cfg.Bucket + ".ci." + cfg.Region + ".myqcloud.com"
	for _, endpoint := range []string{cfg.ObjectURL, cfg.ImageURL} {
		if endpoint != "" && !validTencentEndpoint(endpoint) {
			return nil, command.ErrInvalid
		}
	}
	if cfg.ObjectURL != "" {
		objects = cfg.ObjectURL
	}
	if cfg.ImageURL != "" {
		images = cfg.ImageURL
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CA != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(cfg.CA)) {
			return nil, command.ErrInvalid
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	client := &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:     cfg.SecretID,
			SecretKey:    cfg.SecretKey,
			SessionToken: cfg.SessionToken,
			Expire:       cfg.Timeout + time.Minute,
			Transport:    transport,
		},
		Timeout:       cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Tencent{
		client:         client,
		transport:      transport,
		objects:        objects,
		images:         images,
		bizType:        cfg.BizType,
		maxObjectBytes: cfg.MaxObjectBytes,
		logger:         logger,
	}, nil
}

func validTencentSecret(value string) bool {
	return len(value) >= 1 && len(value) <= 4096 && regexp.MustCompile(`^[\x21-\x7e]+$`).MatchString(value)
}

// Close 释放签名客户端拥有的连接池，不影响其他业务资源。
func (t *Tencent) Close() { t.transport.CloseIdleConnections() }

func (t *Tencent) complete(operation string, start time.Time, err *error, fields ...zap.Field) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	t.logger.Info("腾讯云调用完成", append([]zap.Field{
		zap.String("operation", operation),
		zap.String("result", result),
		zap.Duration("duration", time.Since(start)),
	}, fields...)...)
}

// request 对成功与失败响应均限长，不记录供应商错误正文或图片数据；重试由持久任务负责。
func (t *Tencent) request(ctx context.Context, method, address string, data []byte, headers http.Header, limit int64) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return 0, nil, nil, command.ErrInvalid
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	response, err := t.client.Do(req)
	if err != nil {
		return 0, nil, nil, command.ErrUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit || ctx.Err() != nil {
		clear(raw)
		return 0, nil, nil, command.ErrUnavailable
	}
	return response.StatusCode, response.Header, raw, nil
}

// decodeTencentXML 拒绝尾随文档，避免截断或拼接响应被当成有效结果。
func decodeTencentXML(raw []byte, out any) error {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(out); err != nil {
		return command.ErrUnavailable
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return command.ErrUnavailable
		}
		if data, ok := token.(xml.CharData); !ok || len(bytes.TrimSpace(data)) != 0 {
			return command.ErrUnavailable
		}
	}
}

func (t *Tencent) objectURL(key string) string {
	return t.objects + "/" + url.PathEscape(key)
}

func validTencentEndpoint(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == ""
}
