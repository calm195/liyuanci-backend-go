package entity

import "time"

// WishDetailView 保存一次公开心愿详情打开事件。
type WishDetailView struct {
	ID       int64     `gorm:"column:id;primaryKey;comment:详情打开事件编号" json:"-"`
	WishID   string    `gorm:"column:wish_id;type:uuid;not null;comment:被打开的公开心愿" json:"-"`
	ViewedAt time.Time `gorm:"column:viewed_at;type:timestamptz;not null;comment:详情打开时间" json:"-"`
}

// TableName 返回显式 schema 限定表名。
func (WishDetailView) TableName() string { return "public.wish_detail_views" }
