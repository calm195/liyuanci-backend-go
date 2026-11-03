package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Business 实现待选型的外部密文对象协议。
type Business struct {
	*Client
	maxObjectBytes int64
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

// NewBusiness 绑定外部加密对象预算；对象服务不承担身份、审核或通知职责。
func NewBusiness(client *Client, maxObjectBytes int64) *Business {
	return &Business{Client: client, maxObjectBytes: maxObjectBytes}
}
