package entity

import "time"

// InteractionBlockPolicy 映射匿名黑名单单向策略；修改只通过显式迁移身份入口。
type InteractionBlockPolicy struct {
	Singleton     bool       `gorm:"column:singleton;type:boolean;primaryKey;autoIncrement:false;not null;default:true;comment:唯一全局策略行" json:"-"`
	OpaqueEnabled bool       `gorm:"column:opaque_enabled;type:boolean;not null;default:false;comment:启用后禁止回退目标账号标识接口" json:"-"`
	SwitchedAt    *time.Time `gorm:"column:switched_at;type:timestamptz;comment:已验证历史随机标识后显式切换时间" json:"-"`
}

// TableName 返回显式schema限定表名。
func (InteractionBlockPolicy) TableName() string { return "public.interaction_block_policy" }
