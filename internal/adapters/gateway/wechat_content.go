package gateway

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// CheckPublicText 调用微信文本内容安全 v2，只向业务层传递有限的结果。
func (w *WeChat) CheckPublicText(ctx context.Context, openID string, scene int, content string) (err error) {
	start := time.Now()
	defer func() {
		switch err {
		case nil:
			w.logger.Info("微信文本安全检测完成", zap.Int("scene", scene), zap.Duration("duration", time.Since(start)), zap.String("result", "pass"))
		default:
			w.logger.Info("微信文本安全检测完成", zap.Int("scene", scene), zap.Duration("duration", time.Since(start)), zap.String("result", err.Error()))
		}
	}()
	if openID == "" || scene < 1 || scene > 4 || content == "" {
		return command.ErrUnavailable
	}
	for range 2 {
		token, err := w.codeToken(ctx)
		if err != nil {
			return command.ErrUnavailable
		}
		query := url.Values{"access_token": {token}}
		body, err := w.wechatPost(ctx, wechatAPI+"/wxa/msg_sec_check?"+query.Encode(), map[string]any{
			"version": 2, "openid": openID, "scene": scene, "content": content,
		}, 32<<10)
		if err != nil {
			return command.ErrUnavailable
		}
		rejected, err := parseWeChatTextResult(body)
		clear(body)
		if !rejected {
			return err
		}
		w.codeTokens.mu.Lock()
		if w.codeTokens.value == token {
			w.codeTokens.expiresAt = time.Time{}
		}
		w.codeTokens.mu.Unlock()
	}
	return command.ErrUnavailable
}

// parseWeChatTextResult 区分凭据过期、明确违规、待复核以及不可用。
func parseWeChatTextResult(body []byte) (refresh bool, err error) {
	fields, err := wechatFields(body)
	if err != nil || fields["errcode"] == nil {
		return false, command.ErrUnavailable
	}
	switch wechatErrorCode(fields) {
	case 40001, 40014, 42001:
		return true, command.ErrUnavailable
	case 87014:
		return false, command.ErrContentViolation
	case 0:
	default:
		return false, command.ErrUnavailable
	}
	var result struct {
		Suggest string `json:"suggest"`
	}
	if json.Unmarshal(fields["result"], &result) != nil {
		return false, command.ErrUnavailable
	}
	switch result.Suggest {
	case "pass":
		return false, nil
	case "review":
		return false, command.ErrContentReview
	case "risky":
		return false, command.ErrContentViolation
	default:
		return false, command.ErrUnavailable
	}
}
