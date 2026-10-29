package entity

// WhisperAlias 映射 public.whisper_aliases，传输层不得直接序列化实体。
type WhisperAlias struct {
	PostID  string `gorm:"column:post_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:别名上下文帖子" json:"-"`
	ActorID string `gorm:"column:actor_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:内部参与账号，不输出别名行或账号标识" json:"-"`
	Label   string `gorm:"column:label;type:text;not null;comment:独立随机展示别名，允许重名" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperAlias) TableName() string { return "public.whisper_aliases" }
