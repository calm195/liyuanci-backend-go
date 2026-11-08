package entity

import "time"

// StoryView 保存当前账号与还愿故事之间的唯一浏览记录。
type StoryView struct {
	WishID   string    `gorm:"column:wish_id;type:uuid;primaryKey;autoIncrement:false;comment:已浏览的故事所属心愿" json:"-"`
	ViewerID string    `gorm:"column:viewer_id;type:uuid;primaryKey;autoIncrement:false;comment:已登录的浏览账号" json:"-"`
	ViewedAt time.Time `gorm:"column:viewed_at;type:timestamptz;not null;comment:该访客首次浏览故事时间" json:"-"`
}

// TableName 返回显式 schema 限定表名。
func (StoryView) TableName() string { return "public.story_views" }
