package entity

import "time"

// ResourceCategory 映射资料单级分类。
type ResourceCategory struct {
	ID        string    `gorm:"column:id;type:uuid;not null;comment:稳定分类标识;primaryKey;autoIncrement:false" json:"-"`
	Name      string    `gorm:"column:name;type:text;not null;comment:规范分类名称" json:"-"`
	NameKey   string    `gorm:"column:name_key;type:text;unique;not null;comment:ASCII折叠名称唯一键" json:"-"`
	SortOrder int       `gorm:"column:sort_order;type:integer;not null;comment:显示排序" json:"-"`
	Enabled   bool      `gorm:"column:enabled;type:boolean;not null;comment:是否启用" json:"-"`
	Version   int64     `gorm:"column:version;type:bigint;not null;comment:分类并发版本" json:"-"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;comment:创建时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:timestamptz;not null;comment:修改时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回显式业务表名。
func (ResourceCategory) TableName() string { return "public.resource_categories" }
