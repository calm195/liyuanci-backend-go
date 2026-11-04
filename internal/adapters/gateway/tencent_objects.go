package gateway

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// PutOnce 使用从未启用版本控制的专用桶及禁止覆盖写入；应用版本是完整密文的SHA-256。
// COS开启或暂停版本控制后会忽略禁止覆盖头，因此每次写入前检查并失败关闭。
func (t *Tencent) PutOnce(ctx context.Context, key string, data []byte) (version string, err error) {
	defer t.complete("cos_put", time.Now(), &err)
	if !validObjectKey(key) || len(data) == 0 || int64(len(data)) > t.maxObjectBytes {
		return "", command.ErrInvalid
	}
	status, _, raw, e := t.request(ctx, http.MethodGet, t.objects+"/?versioning", nil, nil, 4096)
	defer clear(raw)
	var state struct {
		XMLName xml.Name `xml:"VersioningConfiguration"`
		Status  []string
	}
	if e != nil || status != http.StatusOK || decodeTencentXML(raw, &state) != nil || len(state.Status) != 0 {
		return "", command.ErrUnavailable
	}
	digest := sha256.Sum256(data)
	version = "sha256:" + hex.EncodeToString(digest[:])
	md5sum := md5.Sum(data)
	headers := http.Header{
		"Content-Type":           {"application/octet-stream"},
		"Content-Md5":            {base64.StdEncoding.EncodeToString(md5sum[:])},
		"X-Cos-Forbid-Overwrite": {"true"},
	}
	status, responseHeaders, body, e := t.request(ctx, http.MethodPut, t.objectURL(key), data, headers, 8192)
	clear(body)
	if e != nil || responseHeaders.Get("x-cos-version-id") != "" || status != http.StatusOK && status != http.StatusConflict {
		return "", command.ErrUnavailable
	}
	// 首次写入、并发冲突与丢失响应后的重试都读取并校验完整密文；不能只信任元数据或ETag。
	reader, e := t.open(ctx, key, version)
	if e != nil {
		return "", e
	}
	defer reader.Close()
	return version, nil
}

// Open 只交付与应用版本摘要完全一致的密文，桶配置或对象被外部更改时不能交付其他内容。
func (t *Tencent) Open(ctx context.Context, key, version string) (reader io.ReadCloser, err error) {
	defer t.complete("cos_open", time.Now(), &err)
	return t.open(ctx, key, version)
}

func (t *Tencent) open(ctx context.Context, key, version string) (io.ReadCloser, error) {
	if !validObjectKey(key) || !strings.HasPrefix(version, "sha256:") || len(version) != 71 {
		return nil, command.ErrInvalid
	}
	if _, err := hex.DecodeString(version[7:]); err != nil {
		return nil, command.ErrInvalid
	}
	status, _, raw, err := t.request(ctx, http.MethodGet, t.objectURL(key), nil, nil, t.maxObjectBytes)
	if err != nil || status != http.StatusOK || len(raw) == 0 {
		clear(raw)
		return nil, command.ErrUnavailable
	}
	digest := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(digest[:]) != version {
		clear(raw)
		return nil, command.ErrUnavailable
	}
	return &privateObject{Reader: bytes.NewReader(raw), data: raw}, nil
}

func validObjectKey(key string) bool {
	return key != "" && len(key) <= 1024 && key != "." && key != ".." && !strings.ContainsAny(key, "\r\n\x00")
}

// privateObject 持有已校验的对象响应，关闭时清除缓冲并阻止继续读取。
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
