package entity

import "time"

// ContentFavorite 只保存本人的内容引用，不保存失效内容的公开副本。
type ContentFavorite struct {
	AccountID  string    `gorm:"column:account_id;type:uuid;primaryKey;autoIncrement:false;comment:收藏所属账号" json:"-"`
	TargetKind string    `gorm:"column:target_kind;primaryKey;comment:内容固定种类" json:"-"`
	TargetID   string    `gorm:"column:target_id;type:uuid;primaryKey;autoIncrement:false;comment:目标内容引用" json:"-"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime:false;comment:首次设置收藏时间" json:"-"`
}

// TableName 返回显式业务表名。
func (ContentFavorite) TableName() string { return "public.content_favorites" }
