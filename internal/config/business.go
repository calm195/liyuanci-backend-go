package config

import (
	"bytes"
	"slices"
	"strings"
	"time"
)

// Business 是完整业务宿主的显式配置；使用本机密钥与词库审核，按进程校验所需身份与凭据。
// Runtime 保存所有业务共用的事务、任务、外部服务和恢复约束。
type Business struct {
	Runtime                                                            Runtime
	SessionKey, CardKey, VerificationKey, WishKey, AdminKey, CrisisKey []byte `json:"-"`
	ModerationKey                                                      []byte `json:"-"`
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

// ValidateBusiness 按用途校验配置，业务密钥与微信凭据仍须显式提供。
func ValidateBusiness(c Business, environment, process string) error {
	if !slices.Contains([]string{"api", "card", "content", "whispers", "account", "evidence", "bootstrap"}, process) ||
		c.Runtime.LocalTest {
		return ErrInvalid
	}
	if _, e := validateRuntime(c.Runtime, environment, true); e != nil {
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
			c.Card.Lease <= c.Runtime.Command.Timeout ||
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
