package entity

import (
	"encoding/json"
	"time"
)

// MatchmakingEnrollment 映射活动报名与审核，授权由领域服务复核。
type MatchmakingEnrollment struct {
	ID             string          `gorm:"column:id;comment:报名随机标识;primaryKey;autoIncrement:false" json:"-"`
	ActivityID     string          `gorm:"column:activity_id;comment:所属活动" json:"-"`
	AccountID      string          `gorm:"column:account_id;comment:报名账号" json:"-"`
	Version        int64           `gorm:"column:version;comment:报名乐观版本" json:"-"`
	ProfileVersion int64           `gorm:"column:profile_version;comment:密文资料版本" json:"-"`
	ProfileCipher  json.RawMessage `gorm:"column:profile_cipher;comment:本人资料和联系方式密文;type:jsonb" json:"-"`
	Status         string          `gorm:"column:status;comment:活动资料审核状态" json:"-"`
	Reason         string          `gorm:"column:reason;comment:审核驳回或退出原因" json:"-"`
	CreatedAt      time.Time       `gorm:"column:created_at;comment:报名时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingEnrollment) TableName() string { return "public.matchmaking_enrollments" }
