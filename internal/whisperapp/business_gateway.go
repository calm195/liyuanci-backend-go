package whisperapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"strings"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// businessGateway 扩展已有TLS供应商协议；生产不装配本地存储、模拟扫描或学校规则。
type businessGateway struct {
	*gateway
	lookupRefs     []string
	maxObjectBytes int64
}

func (g *businessGateway) readyBusiness(ctx context.Context, process string) error {
	ops := []string{"wrap", "unwrap", "redact", "erase"}
	switch process {
	case "api":
		ops = append(ops, "identity_digest", "object_put", "object_open", "image_scan", "school_validate", "public_analysis", "private_classify", "content_analysis", "notify")
	case "bootstrap":
		ops = append(ops, "identity_digest")
	case "card":
		ops = append(ops, "image_scan", "object_put", "object_open")
	case "content":
		ops = append(ops, "content_analysis", "private_classify", "notify")
	}
	return g.capabilities(ctx, ops)
}

func (g *businessGateway) Digest(ctx context.Context, ref, purpose string, value []byte) ([]byte, error) {
	if !slices.Contains(g.lookupRefs, ref) || purpose != "openid" && purpose != "student_number" {
		return nil, command.ErrUnavailable
	}
	var out struct{ Digest []byte }
	e := g.call(ctx, "identity_digest", struct {
		KeyRef, Purpose string
		Value           []byte
	}{ref, purpose, value}, &out)
	if e != nil || len(out.Digest) != 32 {
		clear(out.Digest)
		return nil, command.ErrUnavailable
	}
	return out.Digest, nil
}

func (g *businessGateway) PutOnce(ctx context.Context, key string, data []byte) (string, error) {
	if key == "" || len(data) == 0 || int64(len(data)) > g.maxObjectBytes {
		return "", command.ErrInvalid
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	var out struct{ Version, SHA256 string }
	e := g.call(ctx, "object_put", struct {
		Key, SHA256 string
		Data        []byte
	}{key, hash, data}, &out)
	if e != nil || strings.TrimSpace(out.Version) == "" || out.SHA256 != hash {
		return "", command.ErrUnavailable
	}
	return out.Version, nil
}

func (g *businessGateway) Open(ctx context.Context, key, version string) (io.ReadCloser, error) {
	var out struct {
		Version string
		Data    []byte
	}
	if key == "" || version == "" {
		return nil, command.ErrInvalid
	}
	e := g.callLimit(ctx, "object_open", struct{ Key, Version string }{key, version}, &out, g.maxObjectBytes*2+4096)
	if e != nil || out.Version != version || len(out.Data) == 0 || int64(len(out.Data)) > g.maxObjectBytes {
		clear(out.Data)
		return nil, command.ErrUnavailable
	}
	return &privateObject{Reader: bytes.NewReader(out.Data), data: out.Data}, nil
}

type privateObject struct {
	*bytes.Reader
	data []byte
}

func (o *privateObject) Close() error { clear(o.data); o.Reader.Reset(nil); return nil }

func (g *businessGateway) Scan(ctx context.Context, data []byte, mediaType string) (bool, error) {
	var out struct {
		Approved        *bool
		DetectorVersion string
	}
	e := g.call(ctx, "image_scan", struct {
		Data      []byte
		MediaType string
	}{data, mediaType}, &out)
	if e != nil || out.Approved == nil || strings.TrimSpace(out.DetectorVersion) == "" {
		return false, command.ErrUnavailable
	}
	return *out.Approved, nil
}

func (g *businessGateway) Validate(ctx context.Context, number, name string) error {
	var out struct {
		Valid        *bool
		RulesVersion string
	}
	if e := g.call(ctx, "school_validate", struct{ Name, StudentNumber string }{name, number}, &out); e != nil || out.Valid == nil || strings.TrimSpace(out.RulesVersion) == "" {
		return command.ErrUnavailable
	}
	if !*out.Valid {
		return command.ErrInvalid
	}
	return nil
}

func (g *businessGateway) Analyze(ctx context.Context, in wish.Content) (out moderation.Assessment, err error) {
	err = g.call(ctx, "content_analysis", in, &out)
	if err != nil || strings.TrimSpace(out.Body) == "" || strings.TrimSpace(out.DetectorVersion) == "" {
		return moderation.Assessment{}, command.ErrUnavailable
	}
	return
}

func (g *businessGateway) AnalyzeMessage(ctx context.Context, body string) (moderation.MessageAssessment, error) {
	return g.ClassifyWhisperCapsule(ctx, body)
}
