package entity

import "time"

// ContentViewEvent 只保存短期防重技术键，不关联账号或设备。
type ContentViewEvent struct {
	EventID    string    `gorm:"column:event_id;type:uuid;primaryKey;autoIncrement:false;comment:页面打开的防重随机键" json:"-"`
	TargetKind string    `gorm:"column:target_kind;comment:内容固定种类" json:"-"`
	TargetID   string    `gorm:"column:target_id;type:uuid;comment:内容引用" json:"-"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime:false;comment:短期去重键创建时间" json:"-"`
}

// TableName 返回显式业务表名。
func (ContentViewEvent) TableName() string { return "public.content_view_events" }
