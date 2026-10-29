package entity

import (
	"encoding/json"
	"time"
)

// WhisperComment 映射 public.whisper_comments，传输层不得直接序列化实体。
type WhisperComment struct {
	ID                   string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;not null;comment:随机暖评标识" json:"-"`
	PostID               *string         `gorm:"column:post_id;type:uuid;comment:所属帖子，删除图显式解除后为空" json:"-"`
	AuthorID             string          `gorm:"column:author_id;type:uuid;not null;comment:内部暖评作者" json:"-"`
	Version              int64           `gorm:"column:version;type:bigint;not null;default:1;comment:资源乐观版本" json:"-"`
	ContentVersion       int64           `gorm:"column:content_version;type:bigint;not null;default:1;comment:当前评论正文版本" json:"-"`
	ParentContentVersion int64           `gorm:"column:parent_content_version;type:bigint;not null;default:1;comment:创建或显式重提绑定的父正文版本" json:"-"`
	CommentsEpoch        int64           `gorm:"column:comments_epoch;type:bigint;not null;default:1;comment:提交绑定的父帖评论代次" json:"-"`
	ReviewState          string          `gorm:"column:review_state;type:text;not null;default:'scanning';comment:当前审核状态，不等于实际公开或解锁结果" json:"-"`
	Lifecycle            string          `gorm:"column:lifecycle;type:text;not null;default:'active';comment:业务生命周期，治理和隐私屏障独立判断" json:"-"`
	PrivacyEpoch         int64           `gorm:"column:privacy_epoch;type:bigint;not null;default:0;comment:隐私屏障代次" json:"-"`
	RestrictionEpoch     int64           `gorm:"column:restriction_epoch;type:bigint;not null;default:0;comment:治理限制代次" json:"-"`
	ReviewRound          int64           `gorm:"column:review_round;type:bigint;not null;default:0;comment:独立审核轮次" json:"-"`
	PublishedAt          *time.Time      `gorm:"column:published_at;type:timestamptz;autoCreateTime:false;autoUpdateTime:false;comment:首次或当前最终公开时间" json:"-"`
	CreatedAt            time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间" json:"-"`
	UpdatedAt            time.Time       `gorm:"column:updated_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:最近持久修改时间" json:"-"`
	RecoveryCipher       json.RawMessage `gorm:"column:recovery_cipher;type:jsonb;comment:完成受控脱敏后的幸存评论候选密文" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperComment) TableName() string { return "public.whisper_comments" }
