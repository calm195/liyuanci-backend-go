package entity

// WhisperPreference 映射 public.whisper_preferences，传输层不得直接序列化实体。
type WhisperPreference struct {
	AccountID                string `gorm:"column:account_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:偏好所属账号" json:"-"`
	Version                  int64  `gorm:"column:version;type:bigint;not null;default:1;comment:资源乐观版本" json:"-"`
	ActiveNicknameVersion    *int64 `gorm:"column:active_nickname_version;type:bigint;comment:当前可选已审昵称版本" json:"-"`
	CandidateNicknameVersion *int64 `gorm:"column:candidate_nickname_version;type:bigint;comment:当前候选昵称版本，不替换已批准昵称" json:"-"`
	NextNicknameVersion      int64  `gorm:"column:next_nickname_version;type:bigint;not null;default:1;comment:下一次分配的不可复用昵称版本" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperPreference) TableName() string { return "public.whisper_preferences" }
