package config

import (
	"encoding/base64"
	"net/url"
	"regexp"
)

// WeChatMedia 保存 HTTPS 回调来源与微信公众平台安全模式配置。
type WeChatMedia struct {
	Origin         string
	Token          string
	EncodingAESKey string
}

var wechatPushToken = regexp.MustCompile(`^[A-Za-z0-9]{3,32}$`)

// Enabled 区分未配置和显式配置，部分配置应被拒绝。
func (c WeChatMedia) Enabled() bool {
	return c != (WeChatMedia{})
}

// Validate 校验公开取图域名与微信推送凭据的配置形状。
func (c WeChatMedia) Validate() error {
	if !c.Enabled() {
		return nil
	}
	address, err := url.Parse(c.Origin)
	decoded, keyErr := base64.RawStdEncoding.Strict().DecodeString(c.EncodingAESKey)
	defer clear(decoded)
	if err != nil || address.Scheme != "https" || address.Hostname() == "" ||
		address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" ||
		address.Path != "" || !wechatPushToken.MatchString(c.Token) || keyErr != nil || len(decoded) != 32 {
		return ErrInvalid
	}
	return nil
}
