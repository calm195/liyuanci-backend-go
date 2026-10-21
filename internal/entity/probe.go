package entity

import (
	"time"
)

// FoundationProbeReceipt 映射 public.foundation_probe_receipts，字段注释描述数据库持久化语义。
type FoundationProbeReceipt struct {
	ProbeID       string    `gorm:"column:probe_id;type:varchar(32);primaryKey;autoIncrement:false;comment:随机探针事件标识，作为重复投递的幂等主键" json:"-"`
	DeliveryCount int32     `gorm:"column:delivery_count;type:integer;not null;default:1;comment:同一探针事件实际完成数据库消费的次数" json:"-"`
	ConsumedAt    time.Time `gorm:"column:consumed_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:最近一次消费完成时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (FoundationProbeReceipt) TableName() string { return "public.foundation_probe_receipts" }
