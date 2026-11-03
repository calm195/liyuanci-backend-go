package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"strings"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// Business 扩展已有TLS供应商协议；生产不装配本地存储、模拟扫描或学校规则。
type Business struct {
	*Client
	lookupRefs     []string
	maxObjectBytes int64
}

// ReadyFor 按进程职责核对供应商能力；检查不消费任务或修改业务数据。
func (g *Business) ReadyFor(ctx context.Context, process string) error {
	ops := []string{"wrap", "unwrap", "redact", "erase"}
	switch process {
	case "api":
		ops = append(
			ops,
			"identity_digest",
			"object_put",
			"object_open",
			"image_scan",
			"school_validate",
			"public_analysis",
			"private_classify",
			"content_analysis",
			"notify",
		)
	case "bootstrap":
		ops = append(ops, "identity_digest")
	case "card":
		ops = append(ops, "image_scan", "object_put", "object_open")
	case "content":
		ops = append(ops, "content_analysis", "private_classify", "notify")
	}
	return g.capabilities(ctx, ops)
}

// Digest 仅允许已配置引用及身份用途，拒绝长度异常的检索摘要。
func (g *Business) Digest(ctx context.Context, ref, purpose string, value []byte) ([]byte, error) {
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

// PutOnce 上传不可变密文对象，只有供应商确认相同摘要后才返回版本。
func (g *Business) PutOnce(ctx context.Context, key string, data []byte) (string, error) {
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

// Open 读取精确对象版本；调用方必须关闭返回的读取器以清除缓冲密文。
func (g *Business) Open(ctx context.Context, key, version string) (io.ReadCloser, error) {
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

// Close 清除当前对象的响应缓冲，调用后不能继续读取该版本。
func (o *privateObject) Close() error {

	clear(o.data)
	o.Reader.Reset(nil)
	return nil
}

// Scan 要求供应商返回明确结论与检测版本，缺字段不能视作通过。
func (g *Business) Scan(ctx context.Context, data []byte, mediaType string) (bool, error) {
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

// Validate 检查学校权威结论，不把供应商不可用解释为认证有效。
func (g *Business) Validate(ctx context.Context, number, name string) error {
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

// Analyze 校验公开内容候选与检测版本，保留严格响应字段约束。
func (g *Business) Analyze(ctx context.Context, in wish.Content) (out moderation.Assessment, err error) {
	err = g.call(ctx, "content_analysis", in, &out)
	if err != nil || strings.TrimSpace(out.Body) == "" || strings.TrimSpace(out.DetectorVersion) == "" {
		return moderation.Assessment{}, command.ErrUnavailable
	}
	return
}

// AnalyzeMessage 只分类私聊，不接受供应商替换私密正文。
func (g *Business) AnalyzeMessage(ctx context.Context, body string) (moderation.MessageAssessment, error) {
	return g.ClassifyWhisperCapsule(ctx, body)
}

// NewBusiness 借用已有网关连接，绑定身份检索引用与加密对象预算。
// 调用方先校验对应进程配置；初始化命令不读取对象，允许对象预算为零。
func NewBusiness(client *Client, lookupRefs []string, maxObjectBytes int64) *Business {
	return &Business{client, slices.Clone(lookupRefs), maxObjectBytes}
}

// HasLookupReference 表示当前配置允许使用该检索引用，不会向供应商发起请求。
func (g *Business) HasLookupReference(ref string) bool { return slices.Contains(g.lookupRefs, ref) }
