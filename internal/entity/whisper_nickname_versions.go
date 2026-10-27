package entity

import (
	"encoding/json"
	"time"
)

// WhisperNicknameVersion 映射 public.whisper_nickname_versions，传输层不得直接序列化实体。
type WhisperNicknameVersion struct {
	AccountID         string          `gorm:"column:account_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:昵称所属账号" json:"-"`
	NicknameVersion   int64           `gorm:"column:nickname_version;type:bigint;primaryKey;autoIncrement:false;not null;comment:昵称内容版本" json:"-"`
	ContentRegistryID string          `gorm:"column:content_registry_id;type:uuid;not null;unique;comment:精确内容注册标识，昵称来源使用本随机标识" json:"-"`
	LabelCipher       json.RawMessage `gorm:"column:label_cipher;type:jsonb;not null;comment:用途绑定的昵称原稿信封密文" json:"-"`
	PublicLabel       *string         `gorm:"column:public_label;type:text;comment:审核允许的昵称快照" json:"-"`
	ReviewState       string          `gorm:"column:review_state;type:text;not null;default:'scanning';comment:当前审核状态，不等于实际公开或解锁结果" json:"-"`
	ControlVersion    int64           `gorm:"column:control_version;type:bigint;not null;default:1;comment:昵称版本的治理乐观版本，与偏好版本分离" json:"-"`
	ReviewRound       int64           `gorm:"column:review_round;type:bigint;not null;default:0;comment:独立审核轮次，重提不复用旧工作" json:"-"`
	ReviewTaskID      *string         `gorm:"column:review_task_id;type:uuid;comment:当前审核轮次任务引用，迟到任务不得覆盖" json:"-"`
	RevokedAt         *time.Time      `gorm:"column:revoked_at;type:timestamptz;autoCreateTime:false;autoUpdateTime:false;comment:昵称违规撤销时间，引用内容同步隐藏" json:"-"`
	CreatedAt         time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:昵称版本创建时间" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperNicknameVersion) TableName() string { return "public.whisper_nickname_versions" }
