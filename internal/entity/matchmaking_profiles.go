package entity

import "encoding/json"

// MatchmakingProfile 保存账号维护的密文资料及其乐观版本。
type MatchmakingProfile struct {
	AccountID     string          `gorm:"column:account_id;type:uuid;primaryKey;comment:资料所属账号"`
	Version       int64           `gorm:"column:version;not null;comment:资料乐观版本"`
	ProfileCipher json.RawMessage `gorm:"column:profile_cipher;type:jsonb;not null;comment:账号资料密文"`
}

// TableName 返回账号包分配资料表。
func (MatchmakingProfile) TableName() string { return "public.matchmaking_profiles" }
