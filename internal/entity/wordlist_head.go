package entity

// WordlistHead 保存每类词库的独立并发基准。
type WordlistHead struct {
	Kind        string  `gorm:"column:kind;type:text;primaryKey;comment:固定词库用途类型" json:"kind"`
	Version     int64   `gorm:"column:version;type:bigint;not null;default:1;comment:并发基准代次，与快照资源状态版本独立" json:"version"`
	EffectiveID *string `gorm:"column:effective_id;type:uuid;comment:当前生效不可变快照，无正式发布时为空" json:"effective_id"`
}

// TableName 返回显式词库基准表。
func (WordlistHead) TableName() string { return "public.wordlist_heads" }
