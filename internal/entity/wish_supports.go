package entity

import "time"

// WishSupport 保存当前账号对一条心愿的有效支持。
type WishSupport struct {
	WishID    string    `gorm:"column:wish_id;primaryKey;comment:获得支持的心愿"`
	AccountID string    `gorm:"column:account_id;primaryKey;comment:发出支持的学生账号"`
	CreatedAt time.Time `gorm:"column:created_at;comment:本次支持生效的时间"`
}

// TableName 返回版本迁移定义的支持表。
func (WishSupport) TableName() string { return "public.wish_supports" }
