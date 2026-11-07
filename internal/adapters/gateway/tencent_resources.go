package gateway

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tencentyun/cos-go-sdk-v5"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func validResourceKey(key string) bool {
	id, ok := strings.CutPrefix(key, "resources/")
	if !ok {
		return false
	}
	value, err := uuid.Parse(id)
	return err == nil && value.String() == id
}

// PutResource 写入资料独立命名空间，禁止覆盖已有内容。
func (t *Tencent) PutResource(ctx context.Context, key string, data []byte, media string) (version string, err error) {
	defer t.complete("cos_resource_put", time.Now(), &err)
	if !validResourceKey(key) {
		return "", command.ErrInvalid
	}
	return t.putOnce(ctx, key, data, media, true)
}

// ResourceURL 签发短效附件下载地址，文件名仅作为响应头参数。
func (t *Tencent) ResourceURL(ctx context.Context, key, name string) (string, error) {
	if !validResourceKey(key) {
		return "", command.ErrInvalid
	}
	endpoint, err := url.Parse(t.objects)
	if err != nil {
		return "", command.ErrUnavailable
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: endpoint}, t.client)
	options := &cos.ObjectGetOptions{ResponseContentDisposition: mime.FormatMediaType("attachment", map[string]string{"filename": name})}
	signed, err := client.Object.GetPresignedURL2(ctx, http.MethodGet, key, 5*time.Minute, options)
	if err != nil {
		return "", command.ErrUnavailable
	}
	return signed.String(), nil
}

// RemoveResource 仅清理资料命名空间，已删除对象可重复执行。
func (t *Tencent) RemoveResource(ctx context.Context, key string) (err error) {
	defer t.complete("cos_resource_remove", time.Now(), &err)
	if !validResourceKey(key) {
		return command.ErrInvalid
	}
	if err = t.requireUnversioned(ctx); err != nil {
		return err
	}
	status, headers, body, err := t.request(ctx, http.MethodDelete, t.objectURL(key), nil, nil, 8192)
	defer clear(body)
	if err != nil || headers.Get("x-cos-version-id") != "" || status != http.StatusNoContent && status != http.StatusNotFound {
		return command.ErrUnavailable
	}
	return nil
}
