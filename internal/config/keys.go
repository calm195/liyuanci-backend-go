package config

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"

	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// resolveKeys 仅从主密钥派生用途密钥，不生成随机值或写回配置。
// 环境、用途和v1标签属于持久数据协议；变更标签等同换钥，不能随意重命名。
func (c *Config) resolveKeys() error {
	if len(c.MasterKey) == 0 && c.Mode == "foundation" {
		return nil
	}
	if len(c.MasterKey) != 32 {
		return fmt.Errorf("masterkey必须是32字节随机主密钥的Base64")
	}
	type purposeKey struct {
		purpose string
		target  *[]byte
	}
	b := &c.Business
	keys := []purposeKey{
		{"business/runtime/wrap", &b.Runtime.WrapKey},
		{"business/runtime/recovery-wrap", &b.Runtime.RecoveryWrapKey},
		{"business/lookup", &b.LookupKey},
		{"whispers/wrap", &c.Whispers.WrapKey},
		{"whispers/recovery-wrap", &c.Whispers.RecoveryWrapKey},
		{"business/session", &b.SessionKey},
		{"business/card", &b.CardKey},
		{"business/verification", &b.VerificationKey},
		{"business/wish", &b.WishKey},
		{"business/admin", &b.AdminKey},
		{"business/crisis", &b.CrisisKey},
	}
	for _, scope := range []struct {
		name string
		cfg  *Runtime
	}{{"business/runtime", &b.Runtime}, {"whispers", &c.Whispers}} {
		r := scope.cfg
		for _, key := range []purposeKey{
			{"receipt", &r.ReceiptKey}, {"cursor", &r.CursorKey},
			{"rate", &r.RateKey}, {"governance", &r.GovernanceKey},
			{"privacy", &r.PrivacyKey}, {"evidence", &r.EvidenceKey},
			{"safety", &r.SafetyKey},
		} {
			keys = append(keys, purposeKey{scope.name + "/" + key.purpose, key.target})
		}
	}
	// 配置归组不改变开发数据的派生协议；运行模式继续决定既有用途标签。
	if c.Mode == "development" {
		legacy := map[*[]byte]string{
			&b.Runtime.WrapKey:         "wrap",
			&b.Runtime.RecoveryWrapKey: "recovery-wrap",
			&b.LookupKey:               "lookup",
			&b.SessionKey:              "session",
			&b.CardKey:                 "card",
			&b.VerificationKey:         "verification",
			&b.WishKey:                 "wish",
			&b.AdminKey:                "admin",
		}
		for i := range keys {
			if purpose, ok := legacy[keys[i].target]; ok {
				keys[i].purpose = "development/" + purpose
			}
		}
		keys = append(keys, purposeKey{"development/moderation", &b.ModerationKey})
	}
	for _, key := range keys {
		derived, err := hkdf.Key(sha256.New, c.MasterKey, nil, "liyuanci/config-keys/v1/"+c.Environment+"/"+key.purpose, 32)
		if err != nil {
			return fmt.Errorf("应用密钥派生失败")
		}
		*key.target = derived
	}
	if c.Whispers.LocalTest {
		// 本地组合API共享加密身份、恢复账本和安全索引，必须沿用同一领域的密钥。
		c.Whispers.WrapKey = append([]byte(nil), b.Runtime.WrapKey...)
		c.Whispers.RecoveryWrapKey = append([]byte(nil), b.Runtime.RecoveryWrapKey...)
		c.Whispers.PrivacyKey = securedata.PrivacyReceiptKey(b.WishKey)
		c.Whispers.SafetyKey = securedata.SafetyScopeKey(b.WishKey)
	}
	return nil
}
