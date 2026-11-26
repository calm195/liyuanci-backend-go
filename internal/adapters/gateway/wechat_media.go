package gateway

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

var mediaTrace = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// StartImageCheck 将受控的临时取图地址提交给微信，成功回执仅表示任务受理。
func (w *WeChat) StartImageCheck(ctx context.Context, openID string, scene int, imageURL string) (string, error) {
	for range 2 {
		token, err := w.codeToken(ctx)
		if err != nil {
			return "", err
		}
		query := url.Values{"access_token": {token}}
		body, err := w.wechatPost(ctx, wechatAPI+"/wxa/media_check_async?"+query.Encode(), map[string]any{
			"version": 2, "openid": openID, "scene": scene, "media_type": 2, "media_url": imageURL,
		}, 32<<10)
		if err != nil {
			return "", err
		}
		fields, err := wechatFields(body)
		clear(body)
		if err != nil || fields["errcode"] == nil {
			return "", command.ErrUnavailable
		}
		switch wechatErrorCode(fields) {
		case 0:
			var trace string
			if json.Unmarshal(fields["trace_id"], &trace) == nil && mediaTrace.MatchString(trace) {
				return trace, nil
			}
		case 40001, 40014, 42001:
			w.codeTokens.mu.Lock()
			if w.codeTokens.value == token {
				w.codeTokens.expiresAt = time.Time{}
			}
			w.codeTokens.mu.Unlock()
			continue
		case 87014:
			return "", command.ErrContentViolation
		}
		return "", command.ErrUnavailable
	}
	return "", command.ErrUnavailable
}
