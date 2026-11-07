package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tencentyun/cos-go-sdk-v5"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func validAvatarKey(key string) bool {
	id, ok := strings.CutPrefix(key, "avatars/")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, ".png")
	if !ok {
		return false
	}
	value, err := uuid.Parse(id)
	return err == nil && value.String() == id
}

// PutAvatar 写入独立前缀的私有规范化图片并校验对象完整性。
func (t *Tencent) PutAvatar(ctx context.Context, key string, data []byte) (version string, err error) {
	defer t.complete("cos_avatar_put", time.Now(), &err)
	if !validAvatarKey(key) {
		return "", command.ErrInvalid
	}
	return t.putOnce(ctx, key, data, "image/png", true)
}

// AvatarURL 只签发短效GET授权，签名仅用于当前会话的头像投影。
func (t *Tencent) AvatarURL(ctx context.Context, key string) (string, error) {
	if !validAvatarKey(key) {
		return "", command.ErrInvalid
	}
	endpoint, err := url.Parse(t.objects)
	if err != nil {
		return "", command.ErrUnavailable
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: endpoint}, t.client)
	signed, err := client.Object.GetPresignedURL2(ctx, http.MethodGet, key, 5*time.Minute, nil)
	if err != nil {
		return "", command.ErrUnavailable
	}
	return signed.String(), nil
}

// RemoveAvatar 仅删除独立头像前缀内的对象。已删除对象可重复清理。
func (t *Tencent) RemoveAvatar(ctx context.Context, key string) (err error) {
	defer t.complete("cos_avatar_remove", time.Now(), &err)
	if !validAvatarKey(key) {
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
