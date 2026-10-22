package studentauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// WeChat 使用小程序wx.login取得的code调用固定微信服务端接口；不接受客户端OpenID或任意端点。
// 不持有客户端生命周期；装配方禁止在Transport中记录含凭据的请求URL。
type WeChat struct {
	client        *http.Client
	appID, secret string
	logger        *zap.Logger
}

// NewWeChat 必须注入有界HTTP客户端和忽略目录内或显式外部AppSecret；克隆客户端并禁止跳转，避免凭据随重定向外泄。
func NewWeChat(client *http.Client, appID, secret string, logger *zap.Logger) (*WeChat, error) {
	if client == nil || client.Timeout <= 0 || strings.TrimSpace(appID) == "" || secret == "" || logger == nil {
		return nil, command.ErrInvalid
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &WeChat{client: &clone, appID: appID, secret: secret, logger: logger}, nil
}

// Exchange 只返回后端使用的OpenID；微信session_key/UnionID及供应商错误正文不进入响应、持久层或日志。
func (w *WeChat) Exchange(ctx context.Context, code string) (openid []byte, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrUnavailable
		}
		result := "success"
		if err != nil {
			result = "failure"
			clear(openid)
			openid = nil
		}
		w.logger.Info("微信小程序身份交换完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if strings.TrimSpace(code) == "" || len(code) > 4096 {
		return nil, command.ErrInvalid
	}
	query := url.Values{"appid": {w.appID}, "secret": {w.secret}, "js_code": {code}, "grant_type": {"authorization_code"}}
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.weixin.qq.com/sns/jscode2session?"+query.Encode(), nil)
	if e != nil {
		return nil, command.ErrUnavailable
	}
	response, e := w.client.Do(request)
	if e != nil {
		return nil, command.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, command.ErrUnavailable
	}
	body, e := io.ReadAll(io.LimitReader(response.Body, 32769))
	defer clear(body)
	if e != nil || len(body) > 32768 {
		return nil, command.ErrUnavailable
	}
	fields, e := wechatFields(body)
	if e != nil {
		return nil, command.ErrUnavailable
	}
	defer func() {
		for _, value := range fields {
			clear(value)
		}
	}()
	var codeValue int
	if raw, exists := fields["errcode"]; exists && json.Unmarshal(raw, &codeValue) != nil {
		return nil, command.ErrUnavailable
	}
	if codeValue == 40029 || codeValue == 40163 {
		return nil, command.ErrUnauthenticated
	}
	if codeValue != 0 {
		return nil, command.ErrUnavailable
	}
	var identity string
	if json.Unmarshal(fields["openid"], &identity) != nil || strings.TrimSpace(identity) == "" || len(identity) > 512 {
		return nil, command.ErrUnavailable
	}
	return []byte(identity), nil
}

// wechatFields 拒绝重复键/尾随JSON，避免成功与失败字段产生不同解析含义；未使用的供应商字段只在短期内存中。
func wechatFields(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, e := decoder.Token()
	if e != nil || first != json.Delim('{') {
		return nil, command.ErrUnavailable
	}
	out := map[string]json.RawMessage{}
	for decoder.More() {
		token, e := decoder.Token()
		name, ok := token.(string)
		if e != nil || !ok || out[name] != nil {
			return nil, command.ErrUnavailable
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, command.ErrUnavailable
		}
		out[name] = value
	}
	if _, e = decoder.Token(); e != nil {
		return nil, command.ErrUnavailable
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, command.ErrUnavailable
	}
	return out, nil
}
