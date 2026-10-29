package entity

import "time"

// ResourceFeedback 映射资料当前内容版本学生反馈。
type ResourceFeedback struct {
	ID             string    `gorm:"column:id;type:uuid;not null;comment:反馈技术标识及幂等引用;primaryKey;autoIncrement:false" json:"-"`
	ResourceID     string    `gorm:"column:resource_id;type:uuid;not null;comment:资料标识" json:"-"`
	AccountID      string    `gorm:"column:account_id;type:uuid;not null;comment:反馈主体，注销清理" json:"-"`
	ContentVersion int64     `gorm:"column:content_version;type:bigint;not null;comment:反馈内容版本" json:"-"`
	Kind           string    `gorm:"column:kind;type:text;not null;comment:四类质量反馈" json:"-"`
	Version        int64     `gorm:"column:version;type:bigint;not null;comment:本人反馈并发版本" json:"-"`
	UpdatedAt      time.Time `gorm:"column:updated_at;type:timestamptz;not null;comment:反馈更新时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回显式业务表名。
func (ResourceFeedback) TableName() string { return "public.resource_feedback" }
