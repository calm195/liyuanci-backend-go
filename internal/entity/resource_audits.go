package entity

import (
	"encoding/json"
	"time"
)

// ResourceAudit 映射资料后台变更审计。
type ResourceAudit struct {
	ID           string          `gorm:"column:id;type:uuid;not null;comment:审计标识;primaryKey;autoIncrement:false" json:"-"`
	ActorID      string          `gorm:"column:actor_id;type:uuid;not null;comment:后台操作者" json:"-"`
	TargetID     string          `gorm:"column:target_id;type:uuid;not null;comment:资料或分类技术引用" json:"-"`
	Operation    string          `gorm:"column:operation;type:text;not null;comment:变更动作" json:"-"`
	Version      int64           `gorm:"column:version;type:bigint;not null;comment:变更后版本" json:"-"`
	ReasonCipher json.RawMessage `gorm:"column:reason_cipher;type:jsonb;comment:下架理由密文" json:"-"`
	CreatedAt    time.Time       `gorm:"column:created_at;type:timestamptz;not null;comment:审计时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回显式业务表名。
func (ResourceAudit) TableName() string { return "public.resource_audits" }
