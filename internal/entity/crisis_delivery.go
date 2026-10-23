package entity

import (
	"encoding/json"
	"time"
)

// CrisisDelivery 保存可恢复的危机投递租约，不复制身份正文。
type CrisisDelivery struct {
	ID         string     `gorm:"column:id;type:uuid;primaryKey;comment:随机投递标识"`
	CaseID     string     `gorm:"column:case_id;type:uuid;not null;comment:限定危机案件"`
	Kind       string     `gorm:"column:kind;not null;comment:投递用途"`
	Status     string     `gorm:"column:status;not null;default:pending;comment:持久投递状态"`
	Attempts   int64      `gorm:"column:attempts;not null;comment:租约次数"`
	NextAt     time.Time  `gorm:"column:next_at;not null;default:CURRENT_TIMESTAMP;comment:下次尝试时间"`
	LeaseID    *string    `gorm:"column:lease_id;type:uuid;comment:本次租约令牌"`
	LeaseUntil *time.Time `gorm:"column:lease_until;comment:租约到期时间"`
}

// TableName 返回独立证据域队列。
func (CrisisDelivery) TableName() string { return "evidence.crisis_deliveries" }

// CareCard 是注销时可清理的本人援助资料投影。
type CareCard struct {
	ID          string          `gorm:"column:id;type:uuid;primaryKey;comment:通知目标随机标识"`
	RecipientID string          `gorm:"column:recipient_id;type:uuid;not null;comment:本人账号关联"`
	Resources   json.RawMessage `gorm:"column:resources;type:jsonb;not null;comment:版本化援助资料"`
	CreatedAt   time.Time       `gorm:"column:created_at;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;comment:创建时间"`
}

// TableName 返回普通数据域关怀表。
func (CareCard) TableName() string { return "public.care_cards" }
