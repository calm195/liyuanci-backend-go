package config

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"
)

// Business 是完整业务宿主的显式配置；使用本机密钥与词库审核，按进程校验所需身份与凭据。
// Runtime 复用树洞的密钥、对象服务、恢复日志和角色边界。
type Business struct {
	Runtime                                                            Whispers
	SessionKey, CardKey, VerificationKey, WishKey, AdminKey, CrisisKey []byte `json:"-"`
	LookupKey                                                          []byte `json:"-"`
	WeChatAppID, WeChatSecret, AdminPassword                           string
	SessionTTL, ExchangeTTL, AdminSessionTTL, LoginWindow              time.Duration
	LoginLimit, MaxTextBytes                                           int
	Card                                                               struct {
		MaxBytes, MaxPixels      int64
		DecodeSlots, MaxAttempts int
		Lease, RetryDelay        time.Duration
	}
}

// LoadBusiness 严格解析私有配置，拒绝未知字段、开发模式和缺失依赖；不连接网络或改写文件。
func LoadBusiness(ctx context.Context, path, environment, process string) (cfg Business, err error) {
	if err = ctx.Err(); err != nil {
		return cfg, err
	}
	app, e := Load(path)
	if e != nil {
		return cfg, e
	}
	cfg = app.Business
	err = ValidateBusiness(cfg, environment, process)
	return
}

// ValidateBusiness 按用途校验配置，业务密钥与微信凭据仍须显式提供。
func ValidateBusiness(c Business, environment, process string) error {
	if !slices.Contains([]string{"api", "card", "content", "whispers", "account", "evidence", "bootstrap"}, process) ||
		c.Runtime.LocalTest ||
		c.Runtime.Contact != "" {
		return ErrInvalid
	}
	if _, e := validateWhispers(c.Runtime, environment, true); e != nil {
		return e
	}
	keys := [][]byte{
		c.Runtime.WrapKey,
		c.Runtime.RecoveryWrapKey,
		c.Runtime.ReceiptKey,
		c.Runtime.CursorKey,
		c.Runtime.RateKey,
		c.Runtime.GovernanceKey,
		c.Runtime.PrivacyKey,
		c.Runtime.EvidenceKey,
		c.Runtime.SafetyKey,
	}
	for _, key := range [][]byte{c.SessionKey, c.CardKey, c.VerificationKey, c.WishKey, c.AdminKey, c.CrisisKey, c.LookupKey} {
		if len(key) == 0 {
			continue
		}
		if len(key) != 32 {
			return ErrInvalid
		}
		for _, prior := range keys {
			if bytes.Equal(key, prior) {
				return ErrInvalid
			}
		}
		keys = append(keys, key)
	}
	if process == "api" || process == "bootstrap" {
		if c.Runtime.DatabaseDSN == "" || len(c.LookupKey) != 32 || len(c.AdminKey) != 32 || c.AdminSessionTTL <= 0 || c.LoginWindow <= 0 ||
			c.LoginLimit < 1 {
			return ErrInvalid
		}
		if _, e := ValidateSecret(c.AdminPassword, 12); e != nil {
			return e
		}
	}
	if process == "api" {
		for _, key := range [][]byte{c.SessionKey, c.CardKey, c.VerificationKey, c.WishKey, c.CrisisKey} {
			if len(key) != 32 {
				return ErrInvalid
			}
		}
		if c.WeChatAppID == "" || c.SessionTTL <= 0 || c.ExchangeTTL <= 0 || c.MaxTextBytes < 1 || c.MaxTextBytes > 1<<20 {
			return ErrInvalid
		}
		if _, e := ValidateSecret(c.WeChatSecret, 16); e != nil {
			return e
		}
	}
	if process == "api" || process == "card" {
		if len(c.CardKey) != 32 || c.Card.MaxBytes < 1 || c.Card.MaxBytes > 8<<20 || c.Card.MaxPixels < 1 || c.Card.MaxPixels > 32_000_000 ||
			c.Card.DecodeSlots < 1 ||
			c.Card.DecodeSlots > 16 ||
			c.Card.Lease < c.Runtime.Command.Timeout ||
			c.Card.RetryDelay < time.Millisecond ||
			c.Card.MaxAttempts < 1 ||
			c.Card.MaxAttempts > 1000 {
			return ErrInvalid
		}
	}
	if process == "card" || process == "content" || process == "whispers" {
		if c.Runtime.DatabaseDSN == "" {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateSecret 校验YAML内联的有界单行凭据；不读取外部文件，不生成或替换原值。
func ValidateSecret(value string, minimum int) (string, error) {
	s := strings.TrimSpace(value)
	if len(s) < minimum || len(s) > 4096 || strings.ContainsAny(s, "\r\n\x00") {
		return "", ErrInvalid
	}
	return s, nil
}
