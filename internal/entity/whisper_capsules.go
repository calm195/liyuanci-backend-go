package entity

import (
	"encoding/json"
	"time"
)

// WhisperCapsule 映射 public.whisper_capsules，传输层不得直接序列化实体。
type WhisperCapsule struct {
	ID                string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;not null;comment:随机胶囊标识" json:"-"`
	OwnerID           string          `gorm:"column:owner_id;type:uuid;not null;comment:仅本人日常读取的所有者" json:"-"`
	Version           int64           `gorm:"column:version;type:bigint;not null;default:1;comment:资源乐观版本" json:"-"`
	ContentRegistryID string          `gorm:"column:content_registry_id;type:uuid;not null;unique;comment:精确内容注册标识，昵称来源使用本随机标识" json:"-"`
	BodyCipher        json.RawMessage `gorm:"column:body_cipher;type:jsonb;comment:封存后不可改文，取消或删除清空密文" json:"-"`
	UnlockAt          time.Time       `gorm:"column:unlock_at;type:timestamptz;not null;autoCreateTime:false;autoUpdateTime:false;comment:服务端按秒保存的UTC预约时间" json:"-"`
	ReviewState       string          `gorm:"column:review_state;type:text;not null;default:'scanning';comment:当前审核状态，不等于实际公开或解锁结果" json:"-"`
	Lifecycle         string          `gorm:"column:lifecycle;type:text;not null;default:'active';comment:业务生命周期，治理和隐私屏障独立判断" json:"-"`
	PrivacyEpoch      int64           `gorm:"column:privacy_epoch;type:bigint;not null;default:0;comment:隐私屏障代次" json:"-"`
	RestrictionEpoch  int64           `gorm:"column:restriction_epoch;type:bigint;not null;default:0;comment:治理限制代次" json:"-"`
	ReviewRound       int64           `gorm:"column:review_round;type:bigint;not null;default:0;comment:独立审核轮次" json:"-"`
	ReviewTaskID      *string         `gorm:"column:review_task_id;type:uuid;comment:当前审核轮次任务引用，迟到任务不得覆盖" json:"-"`
	CreatedAt         time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:封存时间" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperCapsule) TableName() string { return "public.whisper_capsules" }
