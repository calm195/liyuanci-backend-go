package gateway

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/imageinput"
)

const (
	wechatAPI          = "https://api.weixin.qq.com"
	wechatCodeMaxBytes = 1 << 20
	wechatCodePixels   = 1 << 20
)

// wechatCodeTokens 合并同一实例的凭据刷新，刷新失败时短暂抑制重试洪峰。
type wechatCodeTokens struct {
	mu         sync.Mutex
	value      string
	expiresAt  time.Time
	retryAfter time.Time
	refreshing chan struct{}
}

// MiniappCode 使用服务端凭据生成指定业务页的小程序码，并将供应商图片规范化为 PNG。
func (w *WeChat) MiniappCode(ctx context.Context, page, scene, env string) (out []byte, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			out, err = nil, command.ErrUnavailable
		}
		result := "success"
		if err != nil {
			result = "failure"
		}
		w.logger.Info("微信小程序码生成完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	_, sceneErr := hex.DecodeString(scene)
	if len(scene) != 32 || sceneErr != nil ||
		(page != "pages/wish-detail/index" && page != "pages/stories/index") ||
		(env != "develop" && env != "trial" && env != "release") {
		return nil, command.ErrInvalid
	}
	// 高峰时快速失败，避免为供应商调用积压过多在途请求。
	select {
	case w.codeSlots <- struct{}{}:
		defer func() { <-w.codeSlots }()
	default:
		return nil, command.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, w.client.Timeout)
	defer cancel()
	for range 2 {
		token, err := w.codeToken(ctx)
		if err != nil {
			return nil, err
		}
		png, rejected, err := w.fetchCode(ctx, token, page, scene, env)
		if !rejected {
			return png, err
		}
		// 只废弃本次使用的缓存；并发请求可能已经更新凭据。
		w.codeTokens.mu.Lock()
		if w.codeTokens.value == token {
			w.codeTokens.expiresAt = time.Time{}
		}
		w.codeTokens.mu.Unlock()
	}
	return nil, command.ErrUnavailable
}

func (w *WeChat) codeToken(ctx context.Context) (string, error) {
	cache := &w.codeTokens
	for {
		cache.mu.Lock()
		now := time.Now()
		if now.Before(cache.expiresAt) {
			value := cache.value
			cache.mu.Unlock()
			return value, nil
		}
		if now.Before(cache.retryAfter) {
			cache.mu.Unlock()
			return "", command.ErrUnavailable
		}
		if wait := cache.refreshing; wait != nil {
			cache.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return "", command.ErrUnavailable
			}
		}
		cache.refreshing = make(chan struct{})
		cache.mu.Unlock()
		return w.refreshCodeToken(ctx)
	}
}

func (w *WeChat) refreshCodeToken(ctx context.Context) (value string, err error) {
	var expiry time.Time
	cache := &w.codeTokens
	defer func() {
		if recover() != nil {
			value, err = "", command.ErrUnavailable
		}
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if err != nil {
			cache.retryAfter = time.Now().Add(3 * time.Second)
		} else {
			cache.value, cache.expiresAt = value, expiry
			cache.retryAfter = time.Time{}
		}
		close(cache.refreshing)
		cache.refreshing = nil
	}()
	// 使用普通模式获取稳定凭据，避免在同一 AppID 的多实例之间主动使令牌失效。
	body, err := w.wechatPost(ctx, wechatAPI+"/cgi-bin/stable_token", map[string]string{
		"grant_type": "client_credential",
		"appid":      w.appID,
		"secret":     w.secret,
	}, 32<<10)
	defer clear(body)
	if err != nil {
		return "", err
	}
	fields, err := wechatFields(body)
	if err != nil {
		return "", err
	}
	var token string
	var seconds int
	if wechatErrorCode(fields) != 0 ||
		json.Unmarshal(fields["access_token"], &token) != nil || token == "" || len(token) > 4096 ||
		strings.ContainsAny(token, " \r\n\t") ||
		json.Unmarshal(fields["expires_in"], &seconds) != nil || seconds <= 0 || seconds > 7200 {
		return "", command.ErrUnavailable
	}
	ttl := time.Duration(seconds) * time.Second
	expiry = time.Now().Add(ttl - min(ttl/2, time.Minute))
	return token, nil
}

func (w *WeChat) fetchCode(ctx context.Context, token, page, scene, env string) ([]byte, bool, error) {
	query := url.Values{"access_token": {token}}
	body, err := w.wechatPost(ctx, wechatAPI+"/wxa/getwxacodeunlimit?"+query.Encode(), map[string]any{
		"page":        page,
		"scene":       scene,
		"env_version": env,
		"check_path":  env == "release",
	}, wechatCodeMaxBytes)
	defer clear(body)
	if err != nil {
		return nil, false, err
	}
	mime := http.DetectContentType(body)
	if mime == "image/png" || mime == "image/jpeg" {
		png, _, err := imageinput.Normalize(bytes.NewReader(body), mime, wechatCodeMaxBytes, wechatCodePixels)
		if err != nil {
			return nil, false, command.ErrUnavailable
		}
		return png, false, nil
	}
	fields, err := wechatFields(body)
	if err != nil {
		return nil, false, command.ErrUnavailable
	}
	code := wechatErrorCode(fields)
	return nil, code == 40001 || code == 40014 || code == 42001, command.ErrUnavailable
}

func wechatErrorCode(fields map[string]json.RawMessage) int {
	if raw, ok := fields["errcode"]; ok {
		var code int
		if json.Unmarshal(raw, &code) != nil || bytes.Equal(raw, []byte("null")) {
			return -1
		}
		return code
	}
	return 0
}

func (w *WeChat) wechatPost(ctx context.Context, endpoint string, input any, limit int64) ([]byte, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, command.ErrUnavailable
	}
	defer clear(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, command.ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(request)
	if err != nil {
		return nil, command.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, command.ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		clear(body)
		return nil, command.ErrUnavailable
	}
	return body, nil
}
