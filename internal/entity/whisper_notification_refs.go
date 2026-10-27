package entity

// WhisperNotificationRef 映射 public.whisper_notification_refs，传输层不得直接序列化实体。
type WhisperNotificationRef struct {
	NotificationID string  `gorm:"column:notification_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:复用站内通知标识" json:"-"`
	Kind           string  `gorm:"column:kind;type:text;not null;comment:通知事件类型" json:"-"`
	SourceID       string  `gorm:"column:source_id;type:uuid;not null;comment:首次公开评论或已解锁胶囊事件标识" json:"-"`
	TargetKind     string  `gorm:"column:target_kind;type:text;not null;comment:受控跳转目标类型" json:"-"`
	TargetID       string  `gorm:"column:target_id;type:uuid;not null;comment:目标实体随机标识，回流必须重验" json:"-"`
	CommentID      *string `gorm:"column:comment_id;type:uuid;comment:评论通知精确引用，胶囊通知为空" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperNotificationRef) TableName() string { return "public.whisper_notification_refs" }
