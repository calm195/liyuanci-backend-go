package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// MediaPush 验证微信安全模式 JSON 推送，并复核加密明文尾部的 AppID。
type MediaPush struct {
	token, appID string
	key          []byte
}

// NewMediaPush 创建指定小程序的安全模式回调验证器。
func NewMediaPush(token, appID, encodingKey string) (*MediaPush, error) {
	key, err := base64.RawStdEncoding.Strict().DecodeString(encodingKey)
	if err != nil || len(key) != 32 || len(token) < 3 || len(token) > 32 || strings.TrimSpace(token) != token || appID == "" {
		return nil, command.ErrInvalid
	}
	return &MediaPush{token: token, appID: appID, key: key}, nil
}

func (p *MediaPush) signature(query url.Values, name string, extra ...string) bool {
	stamp, nonce := query.Get("timestamp"), query.Get("nonce")
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || len(nonce) == 0 || len(nonce) > 128 || len(query[name]) != 1 || len(query["timestamp"]) != 1 || len(query["nonce"]) != 1 {
		return false
	}
	now := time.Now()
	at := time.Unix(seconds, 0)
	if at.Before(now.Add(-10*time.Minute)) || at.After(now.Add(10*time.Minute)) {
		return false
	}
	args := append([]string{p.token, stamp, nonce}, extra...)
	slices.Sort(args)
	sum := sha1.Sum([]byte(strings.Join(args, "")))
	signature, err := hex.DecodeString(query.Get(name))
	return err == nil && subtle.ConstantTimeCompare(signature, sum[:]) == 1
}

// Challenge 校验微信后台首次配置地址的握手。
func (p *MediaPush) Challenge(query url.Values) (string, error) {
	echo := query.Get("echostr")
	if echo == "" || len(echo) > 1024 || len(query["echostr"]) != 1 {
		return "", command.ErrForbidden
	}
	if query.Get("msg_signature") != "" {
		if !p.signature(query, "msg_signature", echo) {
			return "", command.ErrForbidden
		}
		payload, err := p.decrypt(echo)
		defer clear(payload)
		return string(payload), err
	}
	if !p.signature(query, "signature") {
		return "", command.ErrForbidden
	}
	return echo, nil
}

func (p *MediaPush) decrypt(encrypted string) ([]byte, error) {
	data, err := base64.StdEncoding.Strict().DecodeString(encrypted)
	if err != nil || len(data) < 32 || len(data)%aes.BlockSize != 0 {
		return nil, command.ErrForbidden
	}
	defer clear(data)
	block, err := aes.NewCipher(p.key)
	if err != nil {
		return nil, command.ErrForbidden
	}
	cipher.NewCBCDecrypter(block, p.key[:aes.BlockSize]).CryptBlocks(data, data)
	padding := int(data[len(data)-1])
	if padding < 1 || padding > 32 || len(data)-padding < 20 {
		return nil, command.ErrForbidden
	}
	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return nil, command.ErrForbidden
		}
	}
	message := data[:len(data)-padding]
	end := uint64(binary.BigEndian.Uint32(message[16:20])) + 20
	if end > uint64(len(message)) || string(message[end:]) != p.appID {
		return nil, command.ErrForbidden
	}
	return slices.Clone(message[20:end]), nil
}

// Result 只接受已验签的事件。其他微信事件无需写入图片审核状态。
func (p *MediaPush) Result(query url.Values, body []byte) (trace, decision string, err error) {
	wrapper, err := wechatFields(body)
	var encrypted string
	if err != nil || json.Unmarshal(wrapper["Encrypt"], &encrypted) != nil || !p.signature(query, "msg_signature", encrypted) {
		return "", "", command.ErrForbidden
	}
	payload, err := p.decrypt(encrypted)
	if err != nil {
		return "", "", err
	}
	defer clear(payload)
	fields, err := wechatFields(payload)
	if err != nil {
		return "", "", command.ErrInvalid
	}
	var event, appID string
	if json.Unmarshal(fields["Event"], &event) != nil || event != "wxa_media_check" {
		return "", "", nil
	}
	if json.Unmarshal(fields["appid"], &appID) != nil || appID != p.appID ||
		json.Unmarshal(fields["trace_id"], &trace) != nil || !mediaTrace.MatchString(trace) {
		return "", "", command.ErrForbidden
	}
	var version int
	if json.Unmarshal(fields["version"], &version) != nil || version != 2 {
		return "", "", command.ErrInvalid
	}
	if wechatErrorCode(fields) != 0 {
		return trace, "failed", nil
	}
	result, err := wechatFields(fields["result"])
	if err != nil || json.Unmarshal(result["suggest"], &decision) != nil {
		return trace, "failed", nil
	}
	switch decision {
	case "pass", "review", "risky":
		return trace, decision, nil
	default:
		return trace, "failed", nil
	}
}
