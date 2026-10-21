package entity

import (
	"time"
)

// WordlistVersion 映射 public.wordlist_versions，字段注释描述数据库持久化语义。
type WordlistVersion struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	Type      string    `gorm:"column:type;type:text;not null;comment:词库用途类型，实际业务资料待交付" json:"-"`
	Version   int64     `gorm:"column:version;type:bigint;not null;comment:同类型词库版本" json:"-"`
	Status    string    `gorm:"column:status;type:text;not null;default:'draft';comment:词库生命周期" json:"-"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (WordlistVersion) TableName() string { return "public.wordlist_versions" }

// WordlistEntry 映射 public.wordlist_entries，字段注释描述数据库持久化语义。
type WordlistEntry struct {
	ID        string `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	VersionID string `gorm:"column:version_id;type:uuid;not null;comment:所属词库版本" json:"-"`
	Entry     string `gorm:"column:entry;type:text;not null;comment:规则词条，非用户输入" json:"-"`
	Action    string `gorm:"column:action;type:text;not null;comment:命中后的稳定动作码，运营规则交付后验证" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (WordlistEntry) TableName() string { return "public.wordlist_entries" }

// ModerationTask 映射 public.moderation_tasks，字段注释描述数据库持久化语义。
type ModerationTask struct {
	ID                 string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	ContentID          string    `gorm:"column:content_id;type:uuid;not null;comment:注册内容标识" json:"-"`
	ContentVersion     int64     `gorm:"column:content_version;type:bigint;not null;comment:目标内容版本" json:"-"`
	SubjectEpoch       int64     `gorm:"column:subject_epoch;type:bigint;not null;comment:提交时主体权限代次" json:"-"`
	HoldEpoch          int64     `gorm:"column:hold_epoch;type:bigint;not null;default:0;comment:提交时隐私屏障代次" json:"-"`
	Status             string    `gorm:"column:status;type:text;not null;default:'pending';comment:审核任务状态" json:"-"`
	Decision           *string   `gorm:"column:decision;type:text;comment:审核结论，不等于公开或送达结果" json:"-"`
	Reason             *string   `gorm:"column:reason;type:text;comment:审核驳回原因" json:"-"`
	RevisionSuggestion *string   `gorm:"column:revision_suggestion;type:text;comment:独立的修改建议" json:"-"`
	ReviewerID         *string   `gorm:"column:reviewer_id;type:uuid;comment:人工审核账号" json:"-"`
	CreatedAt          time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (ModerationTask) TableName() string { return "public.moderation_tasks" }

// Report 映射 public.reports，字段注释描述数据库持久化语义。
type Report struct {
	ID         string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	ReporterID string    `gorm:"column:reporter_id;type:uuid;not null;comment:举报账号，注销需清理普通关联" json:"-"`
	TargetKind string    `gorm:"column:target_kind;type:text;not null;comment:被举报类型" json:"-"`
	TargetID   string    `gorm:"column:target_id;type:uuid;not null;comment:被举报对象标识，按类型由命令复核" json:"-"`
	TypeCode   string    `gorm:"column:type_code;type:text;not null;comment:交付举报类型清单中的稳定码" json:"-"`
	Status     string    `gorm:"column:status;type:text;not null;default:'pending';comment:举报处置状态" json:"-"`
	HandlerID  *string   `gorm:"column:handler_id;type:uuid;comment:处理人" json:"-"`
	CreatedAt  time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Report) TableName() string { return "public.reports" }

// ContentRestriction 映射 public.content_restrictions，字段注释描述数据库持久化语义。
type ContentRestriction struct {
	ID        string `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	ContentID string `gorm:"column:content_id;type:uuid;not null;comment:受限制的精确内容注册" json:"-"`
	Kind      string `gorm:"column:kind;type:text;not null;comment:限制来源类型" json:"-"`
	SourceRef string `gorm:"column:source_ref;type:uuid;not null;comment:限制来源标识，不复制正文" json:"-"`
	Active    bool   `gorm:"column:active;type:boolean;not null;default:true;comment:该限制原因是否仍有效" json:"-"`
	Version   int64  `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (ContentRestriction) TableName() string { return "public.content_restrictions" }

// PrivacyHold 映射 public.privacy_holds，字段注释描述数据库持久化语义。
type PrivacyHold struct {
	ID                     string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	TargetKind             string     `gorm:"column:target_kind;type:text;not null;comment:受保护的幸存内容类型" json:"-"`
	TargetID               string     `gorm:"column:target_id;type:uuid;not null;comment:幸存内容标识，不保存注销者身份" json:"-"`
	HoldEpoch              int64      `gorm:"column:hold_epoch;type:bigint;not null;comment:屏障代次，旧审核不得解除" json:"-"`
	RequiredContentVersion int64      `gorm:"column:required_content_version;type:bigint;not null;comment:要求重新脱敏审核的内容版本" json:"-"`
	Status                 string     `gorm:"column:status;type:text;not null;default:'active';comment:隐私屏障状态" json:"-"`
	CreatedAt              time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
	ReleasedAt             *time.Time `gorm:"column:released_at;type:timestamptz;comment:经新版本审核复核后的释放时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (PrivacyHold) TableName() string { return "public.privacy_holds" }
