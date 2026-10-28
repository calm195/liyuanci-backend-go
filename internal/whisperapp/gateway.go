package whisperapp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

type gateway struct {
	client              *http.Client
	url, token, version string
	logger              *zap.Logger
}

func newGateway(cfg Settings, log *zap.Logger) (*gateway, error) {
	u, e := url.Parse(cfg.GatewayURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, command.ErrInvalid
	}
	token, e := os.ReadFile(cfg.GatewayTokenFile)
	if e != nil {
		return nil, command.ErrUnavailable
	}
	defer clear(token)
	credential := strings.TrimSpace(string(token))
	if len(credential) < 16 || strings.ContainsAny(credential, "\r\n") {
		return nil, command.ErrInvalid
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.GatewayCAFile != "" {
		pem, e := os.ReadFile(cfg.GatewayCAFile)
		if e != nil {
			return nil, command.ErrUnavailable
		}
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, command.ErrInvalid
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	client := &http.Client{Transport: transport, Timeout: cfg.ProviderTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &gateway{client, cfg.GatewayURL, credential, cfg.ProviderVersion, log}, nil
}
func (g *gateway) close() { g.client.CloseIdleConnections() }
func (g *gateway) call(ctx context.Context, operation string, input, out any) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		g.logger.Info("树洞供应商调用完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
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
	raw, e := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	defer clear(raw)
	if e != nil || len(raw) > 1<<20 {
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
func (g *gateway) ready(ctx context.Context) error {
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
	for _, op := range []string{"wrap", "unwrap", "public_analysis", "private_classify", "redact", "erase", "notify"} {
		if !slices.Contains(out.Capabilities, op) {
			return command.ErrUnavailable
		}
	}
	return nil
}

type gatewayKeys struct {
	g     *gateway
	refs  []string
	scope string
}

func (k gatewayKeys) Wrap(ctx context.Context, key []byte) (string, []byte, error) {
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
func (k gatewayKeys) Unwrap(ctx context.Context, ref string, wrapped []byte) ([]byte, error) {
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
func (g *gateway) AnalyzeWhisper(ctx context.Context, kind, body string, identity whisper.IdentityInput) (out moderation.WhisperAssessment, err error) {
	err = g.call(ctx, "public_analysis", struct {
		Kind, Body string
		Identity   whisper.IdentityInput
	}{kind, body, identity}, &out)
	if err != nil || strings.TrimSpace(out.Body) == "" || out.DetectorVersion == "" {
		return moderation.WhisperAssessment{}, command.ErrUnavailable
	}
	return
}
func (g *gateway) ClassifyWhisperCapsule(ctx context.Context, body string) (out moderation.MessageAssessment, err error) {
	err = g.call(ctx, "private_classify", struct{ Body string }{body}, &out)
	if err != nil || out.DetectorVersion == "" {
		return moderation.MessageAssessment{}, command.ErrUnavailable
	}
	return
}
func (g *gateway) Redact(ctx context.Context, kind, body string) (string, error) {
	var out struct{ Body string }
	e := g.call(ctx, "redact", struct{ Kind, Body string }{kind, body}, &out)
	if e != nil || strings.TrimSpace(out.Body) == "" {
		return "", command.ErrUnavailable
	}
	return out.Body, nil
}
func (g *gateway) Erase(ctx context.Context, in privacy.Intent, objects []privacy.Object) error {
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
func (g *gateway) NotifyCrisis(ctx context.Context, delivery, caseID string) error {
	var out struct{ Completed bool }
	if e := g.call(ctx, "notify", struct{ DeliveryID, CaseID string }{delivery, caseID}, &out); e != nil {
		return e
	}
	if !out.Completed {
		return command.ErrUnavailable
	}
	return nil
}

type catalogFile struct{ path string }

func (c catalogFile) Load(ctx context.Context) (whisper.CatalogSnapshot, error) {
	if e := ctx.Err(); e != nil {
		return whisper.CatalogSnapshot{}, e
	}
	var out ResourcesFile
	if e := readJSON(c.path, &out); e != nil {
		return whisper.CatalogSnapshot{}, e
	}
	if out.Version == "" {
		return whisper.CatalogSnapshot{}, command.ErrUnavailable
	}
	return out.Catalog, nil
}
