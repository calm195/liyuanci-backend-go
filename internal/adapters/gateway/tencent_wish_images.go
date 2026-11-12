package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// RemoveWishImage 仅删除心愿图片独立前缀内的对象，外部删除允许安全重试。
func (t *Tencent) RemoveWishImage(ctx context.Context, key string) (err error) {
	defer t.complete("cos_wish_image_remove", time.Now(), &err)
	id, ok := strings.CutPrefix(key, "wish-images/")
	parsed, e := uuid.Parse(id)
	if !ok || e != nil || parsed.String() != id {
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
