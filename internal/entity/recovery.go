package entity

import (
	"encoding/json"
	"time"
)

// RecoveryDeleteJournal 映射 public.recovery_delete_journal，字段注释描述数据库持久化语义。
type RecoveryDeleteJournal struct {
	RecordNo      int64           `gorm:"column:record_no;type:bigint;primaryKey;autoIncrement:false;->;-:migration;comment:独立日志单调技术序号，不用于聊天送达序号" json:"-"`
	IntentID      string          `gorm:"column:intent_id;type:uuid;not null;unique;comment:随机删除意图标识，用于重复写入去重" json:"-"`
	DeletionScope string          `gorm:"column:deletion_scope;type:text;not null;comment:技术删除范围，不包含身份或正文" json:"-"`
	EntryEnvelope json.RawMessage `gorm:"column:entry_envelope;type:jsonb;not null;comment:随机实体及对象标识的信封密文，禁止明文身份、正文或浏览记录" json:"-"`
	HappenedAt    time.Time       `gorm:"column:happened_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:删除意图持久化的技术时间" json:"-"`
	ConfirmedAt   *time.Time      `gorm:"column:confirmed_at;type:timestamptz;comment:对应删除工作确认时间，非恢复开放授权" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (RecoveryDeleteJournal) TableName() string { return "public.recovery_delete_journal" }

// RecoverySchemaMigration 映射 public.recovery_schema_migrations，字段注释描述数据库持久化语义。
type RecoverySchemaMigration struct {
	Version   int64     `gorm:"column:version;type:bigint;primaryKey;autoIncrement:false;comment:迁移版本" json:"-"`
	Checksum  string    `gorm:"column:checksum;type:text;not null;comment:迁移校验和" json:"-"`
	AppliedAt time.Time `gorm:"column:applied_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:迁移应用时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (RecoverySchemaMigration) TableName() string { return "public.recovery_schema_migrations" }
