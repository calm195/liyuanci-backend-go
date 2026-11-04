package moderation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// cursor 使用现有签名格式绑定主体和查询用途，防止审核列表游标跨岗位主体复用。
func (s *Service) cursor(actor, purpose, id string) string {
	payload := actor + "|" + purpose + "|" + id
	mac := hmac.New(sha256.New, s.cfg.CursorKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(append([]byte(payload+"|"), mac.Sum(nil)...))
}

func (s *Service) after(actor, purpose, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 512 {
		return "", command.ErrInvalid
	}
	raw, e := base64.RawURLEncoding.DecodeString(cursor)
	if e != nil || len(raw) < 33 {
		return "", command.ErrInvalid
	}
	payload, signature := raw[:len(raw)-33], raw[len(raw)-32:]
	parts := strings.Split(string(payload), "|")
	mac := hmac.New(sha256.New, s.cfg.CursorKey)
	mac.Write(payload)
	if raw[len(raw)-33] != '|' || len(parts) != 3 || parts[0] != actor || parts[1] != purpose || !validID(parts[2]) ||
		!hmac.Equal(signature, mac.Sum(nil)) {
		return "", command.ErrInvalid
	}
	return parts[2], nil
}

// digest 生成幂等请求摘要；JSON编码及密钥用途必须保持兼容，不能随文件重组改变。
func (s *Service) digest(value any) string {
	b, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, s.cfg.CursorKey)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
