package entity

// ContentCommentNotice 以评论事件和接收者去重，不保存正文或公开作者身份。
type ContentCommentNotice struct {
	CommentID      string `gorm:"column:comment_id;type:uuid;primaryKey;autoIncrement:false;comment:首次公开的心愿评论" json:"-"`
	RecipientID    string `gorm:"column:recipient_id;type:uuid;primaryKey;autoIncrement:false;comment:通知接收者" json:"-"`
	NotificationID string `gorm:"column:notification_id;type:uuid;not null;comment:全站通知引用" json:"-"`
}

// TableName 返回显式业务表名。
func (ContentCommentNotice) TableName() string { return "public.content_comment_notices" }
