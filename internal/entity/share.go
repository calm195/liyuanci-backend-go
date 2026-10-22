package entity

import "time"

// ShareLink 映射无正文快照的分享技术引用。
type ShareLink struct {
	ID         string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机分享引用" json:"-"`
	TargetKind string    `gorm:"column:target_kind;type:text;not null;comment:目标类型" json:"-"`
	TargetID   string    `gorm:"column:target_id;type:uuid;not null;comment:内容目标标识" json:"-"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime:false;autoUpdateTime:false;comment:创建时间" json:"-"`
}

// TableName 返回固定表名。
func (ShareLink) TableName() string { return "public.share_links" }
