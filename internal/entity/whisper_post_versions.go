package entity

import (
	"encoding/json"
	"time"
)

// WhisperPostVersion 映射 public.whisper_post_versions，传输层不得直接序列化实体。
type WhisperPostVersion struct {
	PostID            string          `gorm:"column:post_id;type:uuid;primaryKey;autoIncrement:false;not null;comment:所属帖子" json:"-"`
	ContentVersion    int64           `gorm:"column:content_version;type:bigint;primaryKey;autoIncrement:false;not null;comment:正文版本" json:"-"`
	ContentRegistryID string          `gorm:"column:content_registry_id;type:uuid;not null;unique;comment:精确内容注册标识，昵称来源使用本随机标识" json:"-"`
	BodyCipher        json.RawMessage `gorm:"column:body_cipher;type:jsonb;not null;comment:用途绑定的正文信封密文" json:"-"`
	CandidateCipher   json.RawMessage `gorm:"column:candidate_cipher;type:jsonb;comment:待审脱敏候选密文" json:"-"`
	IdentityMode      string          `gorm:"column:identity_mode;type:text;not null;comment:本版本选择的匿名档位" json:"-"`
	IdentityCipher    json.RawMessage `gorm:"column:identity_cipher;type:jsonb;not null;comment:自选标签或昵称输入的用途绑定密文" json:"-"`
	NicknameVersion   *int64          `gorm:"column:nickname_version;type:bigint;comment:固定昵称快照所引用的版本" json:"-"`
	DictionaryVersion *int64          `gorm:"column:dictionary_version;type:bigint;comment:半匿名受控字典版本" json:"-"`
	ReviewState       string          `gorm:"column:review_state;type:text;not null;default:'draft';comment:当前审核状态，不等于实际公开或解锁结果" json:"-"`
	ReviewTaskID      *string         `gorm:"column:review_task_id;type:uuid;comment:当前审核轮次任务引用，迟到任务不得覆盖" json:"-"`
	PublicIdentity    json.RawMessage `gorm:"column:public_identity;type:jsonb;comment:当前已审最小身份快照，可撤销" json:"-"`
	PublicBody        *string         `gorm:"column:public_body;type:text;comment:当前已审脱敏正文，非私有原文" json:"-"`
	SchoolRelated     *bool           `gorm:"column:school_related;type:boolean;comment:已完成涉校判断结果，未完成为空" json:"-"`
	NoticeCodes       json.RawMessage `gorm:"column:notice_codes;type:jsonb;comment:结构化审核展示提示" json:"-"`
	CreatedAt         time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:正文版本创建时间" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperPostVersion) TableName() string { return "public.whisper_post_versions" }
