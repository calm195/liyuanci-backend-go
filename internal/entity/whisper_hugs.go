package entity

// WhisperHug 映射 public.whisper_hugs，传输层不得直接序列化实体。
type WhisperHug struct {
	PostID  string `gorm:"column:post_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:目标帖子" json:"-"`
	ActorID string `gorm:"column:actor_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:内部互动账号" json:"-"`
	Active  bool   `gorm:"column:active;type:boolean;not null;default:true;comment:当前有效目标态，取消保留记录" json:"-"`
	Version int64  `gorm:"column:version;type:bigint;not null;default:1;comment:独立抱抱版本，不使用帖子版本或计数" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperHug) TableName() string { return "public.whisper_hugs" }
