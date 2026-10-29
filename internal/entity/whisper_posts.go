package entity

import (
	"time"
)

// WhisperPost 映射 public.whisper_posts，传输层不得直接序列化实体。
type WhisperPost struct {
	ID                      string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;not null;comment:随机帖子标识" json:"-"`
	OwnerID                 string     `gorm:"column:owner_id;type:uuid;not null;comment:内部作者账号，不向公共投影输出" json:"-"`
	Version                 int64      `gorm:"column:version;type:bigint;not null;default:1;comment:资源乐观版本" json:"-"`
	CurrentContentVersion   int64      `gorm:"column:current_content_version;type:bigint;not null;default:1;comment:当前正文版本" json:"-"`
	PublishedContentVersion *int64     `gorm:"column:published_content_version;type:bigint;comment:当前公开正文版本，未公开为空" json:"-"`
	Lifecycle               string     `gorm:"column:lifecycle;type:text;not null;default:'active';comment:业务生命周期，治理和隐私屏障独立判断" json:"-"`
	ReviewState             string     `gorm:"column:review_state;type:text;not null;default:'draft';comment:当前审核状态，不等于实际公开或解锁结果" json:"-"`
	PublishIntent           bool       `gorm:"column:publish_intent;type:boolean;not null;default:false;comment:显式提交授予当前轮审核后自动公开意愿" json:"-"`
	CommentsOpen            bool       `gorm:"column:comments_open;type:boolean;not null;default:true;comment:评论写入开关，关闭不删除已公开评论" json:"-"`
	CommentsEpoch           int64      `gorm:"column:comments_epoch;type:bigint;not null;default:1;comment:评论上下文代次" json:"-"`
	PrivacyEpoch            int64      `gorm:"column:privacy_epoch;type:bigint;not null;default:0;comment:隐私屏障代次" json:"-"`
	RestrictionEpoch        int64      `gorm:"column:restriction_epoch;type:bigint;not null;default:0;comment:治理限制代次" json:"-"`
	ReviewRound             int64      `gorm:"column:review_round;type:bigint;not null;default:0;comment:独立送审轮次" json:"-"`
	CreatedAt               time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，本人列表排序依据" json:"-"`
	UpdatedAt               time.Time  `gorm:"column:updated_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:最近持久状态修改时间" json:"-"`
	PublishedAt             *time.Time `gorm:"column:published_at;type:timestamptz;autoCreateTime:false;autoUpdateTime:false;comment:当前版本最终公开时间" json:"-"`
}

// TableName 返回显式schema限定表名。
func (WhisperPost) TableName() string { return "public.whisper_posts" }
