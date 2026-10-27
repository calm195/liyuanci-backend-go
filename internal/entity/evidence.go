package entity

import (
	"encoding/json"
	"time"
)

// EvidenceCase 映射 evidence.evidence_cases，字段注释描述数据库持久化语义。
type EvidenceCase struct {
	Version   int64     `gorm:"column:version;not null;default:1;comment:证据范围版本，删除命令绑定此版本" json:"-"`
	ID        string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	Kind      string    `gorm:"column:kind;type:text;not null;comment:证据范围来源" json:"-"`
	SourceRef string    `gorm:"column:source_ref;type:uuid;not null;comment:来源事件技术标识，不外键绑定普通数据" json:"-"`
	Status    string    `gorm:"column:status;type:text;not null;default:'open';comment:证据案件状态" json:"-"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (EvidenceCase) TableName() string { return "evidence.evidence_cases" }

// EvidenceCrisisCase 映射 evidence.crisis_cases，字段注释描述数据库持久化语义。
type EvidenceCrisisCase struct {
	SourceEntityID       *string    `gorm:"column:source_entity_id;type:uuid;comment:树洞案件的安全来源随机标识，普通数据删除后仍可限定投影" json:"-"`
	SourceContentVersion *int64     `gorm:"column:source_content_version;type:bigint;comment:树洞案件精确正文版本，不随普通数据删除或换版变化" json:"-"`
	IdentityStatus       *string    `gorm:"column:identity_status;type:text;comment:检测时认证身份状态，历史未记录为空" json:"-"`
	Version              int64      `gorm:"column:version;not null;default:1;comment:危机案件独立乐观版本" json:"-"`
	SourceTaskID         *string    `gorm:"column:source_task_id;type:uuid;comment:检测时审核任务技术引用" json:"-"`
	HandoffAt            *time.Time `gorm:"column:handoff_at;type:timestamptz;comment:北京时间人工接手窗口起点" json:"-"`
	ID                   string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	EvidenceCaseID       string     `gorm:"column:evidence_case_id;type:uuid;not null;unique;comment:证据范围" json:"-"`
	ContentType          string     `gorm:"column:content_type;type:text;not null;comment:危机来源内容类型" json:"-"`
	Status               string     `gorm:"column:status;type:text;not null;default:'isolated';comment:危机处理状态" json:"-"`
	DetectedAt           time.Time  `gorm:"column:detected_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:检测时间" json:"-"`
	Resolution           *string    `gorm:"column:resolution;type:text;comment:安全处理结论码，不复制敏感正文" json:"-"`
	HandlerID            *string    `gorm:"column:handler_id;type:uuid;comment:授权处理人技术标识，不依赖普通账号或后台外键存续" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (EvidenceCrisisCase) TableName() string { return "evidence.crisis_cases" }

// EvidenceAsset 映射 evidence.assets，字段注释描述数据库持久化语义。
type EvidenceAsset struct {
	ID                 string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	CaseID             string          `gorm:"column:case_id;type:uuid;not null;comment:限定证据案件" json:"-"`
	EncryptedObjectKey json.RawMessage `gorm:"column:encrypted_object_key;type:jsonb;not null;comment:证据私有对象键，保存信封密文而非明文" json:"-"`
	ObjectVersion      string          `gorm:"column:object_version;type:text;not null;comment:证据对象版本" json:"-"`
	WrappedKeyRef      string          `gorm:"column:wrapped_key_ref;type:text;not null;comment:证据专用包裹密钥引用" json:"-"`
	Status             string          `gorm:"column:status;type:text;not null;default:'active';comment:证据对象状态" json:"-"`
	CreatedAt          time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (EvidenceAsset) TableName() string { return "evidence.assets" }

// EvidenceItem 映射 evidence.evidence_items，字段注释描述数据库持久化语义。
type EvidenceItem struct {
	ID            string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	CaseID        string          `gorm:"column:case_id;type:uuid;not null;comment:限定证据范围" json:"-"`
	SubjectCipher json.RawMessage `gorm:"column:subject_cipher;type:jsonb;not null;comment:限定证据主体，保存信封密文而非明文" json:"-"`
	BodyCipher    json.RawMessage `gorm:"column:body_cipher;type:jsonb;not null;comment:限定证据正文，保存信封密文而非明文" json:"-"`
	AssetRef      *string         `gorm:"column:asset_ref;type:uuid;comment:可选证据域对象标识" json:"-"`
	KeyRef        string          `gorm:"column:key_ref;type:text;not null;comment:证据外部密钥引用，不存密钥材料" json:"-"`
	CreatedAt     time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (EvidenceItem) TableName() string { return "evidence.evidence_items" }

// EvidenceAction 映射 evidence.evidence_actions，字段注释描述数据库持久化语义。
type EvidenceAction struct {
	ReasonCipher json.RawMessage `gorm:"column:reason_cipher;type:jsonb;comment:处置理由信封密文" json:"-"`
	ID           string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	CaseID       string          `gorm:"column:case_id;type:uuid;not null;comment:限定案件" json:"-"`
	Actor        string          `gorm:"column:actor;type:uuid;not null;comment:后台处理者技术标识，不依赖普通账号存续" json:"-"`
	Action       string          `gorm:"column:action;type:text;not null;comment:必要处置动作码，不存身份正文" json:"-"`
	At           time.Time       `gorm:"column:at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:处置时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (EvidenceAction) TableName() string { return "evidence.evidence_actions" }
