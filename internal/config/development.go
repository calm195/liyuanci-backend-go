package config

import "time"
import "github.com/calm195/liyuanci-backend-go/internal/moderation"

// Development 保存仅开发环境使用的本地适配器配置。
type Development struct {
	// LocalTest 明确允许本地业务使用同一受限登录，授权继续按服务端会话执行。
	LocalTest                        bool          `json:"-"`
	DatabaseDSN                      string        `json:"-"`
	DatabaseMaxOpen, DatabaseMaxIdle int           `json:"-"`
	DatabaseMaxLifetime              time.Duration `json:"-"`
	JournalFile                      string
	// 以下密钥由顶层masterkey派生，YAML不能单独设置。
	RecoveryWrapKey []byte `json:"-"`
	Care            *moderation.CareResources
	// ModerationKey 使用独立用途派生；关怀资料仍需显式核验提供。
	ModerationKey                                                               []byte `json:"-"`
	WrapKey, LookupKey, SessionKey, CardKey, VerificationKey, WishKey, AdminKey []byte `json:"-"`
	AdminPassword, WeChatAppID, WeChatSecret                                    string
	ObjectDirectory                                                             string
}
