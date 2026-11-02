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
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
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
	client := &http.Client{Transport: transport, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
		g.logger.Info("业务供应商调用完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
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

// Ready 核对供应商版本与树洞、恢复所需能力，缺项时失败关闭。
func (g *Client) Ready(ctx context.Context) error {
	return g.capabilities(ctx, []string{"wrap", "unwrap", "public_analysis", "private_classify", "redact", "erase", "notify"})
}

func (g *Client) capabilities(ctx context.Context, required []string) error {
	var out struct {
		Version      string
		Capabilities []string
	}
	if e := g.call(ctx, "health", struct{}{}, &out); e != nil {
		return e
	}
	if out.Version != g.version {
		return command.ErrUnavailable
	}
	for _, op := range required {
		if !slices.Contains(out.Capabilities, op) {
			return command.ErrUnavailable
		}
	}
	return nil
}

// Keys 将KMS调用绑定到固定用途和允许的历史引用；用途字符串属于持久协议。
type Keys struct {
	g     *Client
	refs  []string
	scope string
}

// Wrap 仅使用当前引用封装数据密钥，不接受供应商静默切换引用。
func (k Keys) Wrap(ctx context.Context, key []byte) (string, []byte, error) {
	var out struct {
		KeyRef  string
		Wrapped []byte
	}
	e := k.g.call(ctx, "wrap", struct {
		Scope, KeyRef string
		Key           []byte
	}{k.scope, k.refs[0], key}, &out)
	if e != nil || out.KeyRef != k.refs[0] || len(out.Wrapped) == 0 {
		return "", nil, command.ErrUnavailable
	}
	return out.KeyRef, out.Wrapped, nil
}

// Unwrap 拒绝未登记历史引用，业务用途与恢复用途不能互换。
func (k Keys) Unwrap(ctx context.Context, ref string, wrapped []byte) ([]byte, error) {
	if !slices.Contains(k.refs, ref) {
		return nil, command.ErrUnavailable
	}
	var out struct{ Key []byte }
	e := k.g.call(ctx, "unwrap", struct {
		Scope, KeyRef string
		Wrapped       []byte
	}{k.scope, ref, wrapped}, &out)
	if e != nil || len(out.Key) != 32 {
		clear(out.Key)
		return nil, command.ErrUnavailable
	}
	return out.Key, nil
}

// AnalyzeWhisper 返回经过结构校验的公开树洞审核候选。
func (g *Client) AnalyzeWhisper(ctx context.Context, kind, body string, identity whisper.IdentityInput) (out moderation.WhisperAssessment, err error) {
	err = g.call(ctx, "public_analysis", struct {
		Kind, Body string
		Identity   whisper.IdentityInput
	}{kind, body, identity}, &out)
	if err != nil || strings.TrimSpace(out.Body) == "" || out.DetectorVersion == "" {
		return moderation.WhisperAssessment{}, command.ErrUnavailable
	}
	return
}

// ClassifyWhisperCapsule 只接收胶囊分类结论，不允许响应携带替换正文。
func (g *Client) ClassifyWhisperCapsule(ctx context.Context, body string) (out moderation.MessageAssessment, err error) {
	err = g.call(ctx, "private_classify", struct{ Body string }{body}, &out)
	if err != nil || out.DetectorVersion == "" {
		return moderation.MessageAssessment{}, command.ErrUnavailable
	}
	return
}

// Redact 要求非空脱敏结果，失败时保留调用方的恢复门禁。
func (g *Client) Redact(ctx context.Context, kind, body string) (string, error) {
	var out struct{ Body string }
	e := g.call(ctx, "redact", struct{ Kind, Body string }{kind, body}, &out)
	if e != nil || strings.TrimSpace(out.Body) == "" {
		return "", command.ErrUnavailable
	}
	return out.Body, nil
}

// Erase 只有全副本清理及写屏障均被确认时才报告成功。
func (g *Client) Erase(ctx context.Context, in privacy.Intent, objects []privacy.Object) error {
	var out struct{ Completed, WriteBarrierConfirmed bool }
	if e := g.call(ctx, "erase", struct {
		Intent  privacy.Intent
		Objects []privacy.Object
	}{in, objects}, &out); e != nil {
		return e
	}
	if !out.Completed || !out.WriteBarrierConfirmed {
		return command.ErrUnavailable
	}
	return nil
}

// NotifyCrisis 只发送稳定投递标识与案件标识；供应商须据此幂等处理。
func (g *Client) NotifyCrisis(ctx context.Context, delivery, caseID string) error {
	var out struct{ Completed bool }
	if e := g.call(ctx, "notify", struct{ DeliveryID, CaseID string }{delivery, caseID}, &out); e != nil {
		return e
	}
	if !out.Completed {
		return command.ErrUnavailable
	}
	return nil
}

// NewKeys 借用客户端并复制引用清单；不改变business/recovery用途或密钥顺序。
func NewKeys(client *Client, refs []string, scope string) Keys {
	return Keys{client, slices.Clone(refs), scope}
}
