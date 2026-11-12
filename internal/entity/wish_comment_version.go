package entity

import (
	"encoding/json"
	"time"
)

// WishCommentVersion 保存逐版原稿及审核后的公开正文。
type WishCommentVersion struct {
	CommentID         string          `gorm:"column:comment_id;type:uuid;primaryKey;autoIncrement:false;comment:所属评论" json:"-"`
	ContentVersion    int64           `gorm:"column:content_version;primaryKey;autoIncrement:false;comment:不可变正文版本" json:"-"`
	ContentRegistryID string          `gorm:"column:content_registry_id;type:uuid;not null;comment:精确内容注册" json:"-"`
	BodyCipher        json.RawMessage `gorm:"column:body_cipher;type:jsonb;not null;comment:原稿信封密文" json:"-"`
	PublicBody        *string         `gorm:"column:public_body;comment:当前经审核的公开正文" json:"-"`
	ReviewTaskID      string          `gorm:"column:review_task_id;type:uuid;not null;comment:本版审核任务" json:"-"`
	CreatedAt         time.Time       `gorm:"column:created_at;autoCreateTime:false;comment:版本创建时间" json:"-"`
}

// TableName 返回显式业务表名。
func (WishCommentVersion) TableName() string { return "public.wish_comment_versions" }
