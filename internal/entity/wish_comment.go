package entity

import "time"

// WishComment 保存心愿评论、作者补充及楼内引用，正文另以版本存储。
type WishComment struct {
	ID                   string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机评论标识" json:"-"`
	WishID               string     `gorm:"column:wish_id;type:uuid;not null;comment:所属心愿" json:"-"`
	AuthorID             string     `gorm:"column:author_id;type:uuid;not null;comment:内部评论作者" json:"-"`
	Kind                 string     `gorm:"column:kind;not null;default:'comment';comment:普通评论或作者补充" json:"-"`
	RootCommentID        *string    `gorm:"column:root_comment_id;type:uuid;comment:楼内回复所属根评论" json:"-"`
	ReplyToCommentID     *string    `gorm:"column:reply_to_comment_id;type:uuid;comment:被回复评论引用，不保存身份副本" json:"-"`
	Version              int64      `gorm:"column:version;not null;default:1;comment:评论资源乐观版本" json:"-"`
	ContentVersion       int64      `gorm:"column:content_version;not null;default:1;comment:当前正文版本" json:"-"`
	ParentContentVersion int64      `gorm:"column:parent_content_version;not null;comment:绑定心愿正文版本" json:"-"`
	CommentsEpoch        int64      `gorm:"column:comments_epoch;not null;comment:绑定心愿评论代次" json:"-"`
	Status               string     `gorm:"column:status;not null;default:'pending_scan';comment:评论审核和撤回状态" json:"-"`
	PublishedAt          *time.Time `gorm:"column:published_at;comment:当前正文公开时间" json:"-"`
	CreatedAt            time.Time  `gorm:"column:created_at;autoCreateTime:false;comment:首次创建时间" json:"-"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;autoUpdateTime:false;comment:最近修改时间" json:"-"`
}

// TableName 返回显式业务表名。
func (WishComment) TableName() string { return "public.wish_comments" }
