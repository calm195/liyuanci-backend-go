package entity

import (
	"encoding/json"
	"time"
)

// IdentityLookupKey 映射 public.identity_lookup_keys，字段注释描述数据库持久化语义。
type IdentityLookupKey struct {
	Purpose     string  `gorm:"column:purpose;type:text;primaryKey;autoIncrement:false;comment:检索用途：学号或微信身份" json:"-"`
	CurrentRef  string  `gorm:"column:current_ref;type:text;not null;comment:当前写入和回填的外部检索密钥引用" json:"-"`
	PreviousRef *string `gorm:"column:previous_ref;type:text;comment:轮换期间仍参与查重的旧代引用，回填完成前不得清除" json:"-"`
	Version     int64   `gorm:"column:version;type:bigint;not null;default:1;comment:轮换状态版本，初始化及每次状态切换递增" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (IdentityLookupKey) TableName() string { return "public.identity_lookup_keys" }

// IdentitySecret 映射 public.identity_secrets，字段注释描述数据库持久化语义。
type IdentitySecret struct {
	AccountID      string          `gorm:"column:account_id;type:uuid;primaryKey;autoIncrement:false;comment:拥有该身份字段的普通账号，普通账号物理删除时清理" json:"-"`
	Purpose        string          `gorm:"column:purpose;type:text;primaryKey;autoIncrement:false;comment:身份字段用途，同时作为认证加密绑定及检索域" json:"-"`
	ContentVersion int64           `gorm:"column:content_version;type:bigint;not null;comment:密文内容版本，与认证加密附加数据一致" json:"-"`
	Envelope       json.RawMessage `gorm:"column:envelope;type:jsonb;not null;comment:算法、外部密钥引用、包裹数据密钥、随机数和认证密文" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (IdentitySecret) TableName() string { return "public.identity_secrets" }

// IdentityLookupClaim 映射 public.identity_lookup_claims，字段注释描述数据库持久化语义。
type IdentityLookupClaim struct {
	Purpose   string `gorm:"column:purpose;type:text;primaryKey;autoIncrement:false;comment:用途域，学号与微信身份分别保持唯一" json:"-"`
	KeyRef    string `gorm:"column:key_ref;type:text;primaryKey;autoIncrement:false;comment:计算该检索摘要的外部密钥引用" json:"-"`
	Digest    []byte `gorm:"column:digest;type:bytea;primaryKey;autoIncrement:false;comment:独立检索密钥计算的用途隔离 HMAC-SHA256，禁止写日志" json:"-"`
	AccountID string `gorm:"column:account_id;type:uuid;not null;comment:唯一占用账号，与对应密文同行生命周期清理" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (IdentityLookupClaim) TableName() string { return "public.identity_lookup_claims" }

// IdentityName 映射 public.identity_names，字段注释描述数据库持久化语义。
type IdentityName struct {
	AccountID      string          `gorm:"column:account_id;type:uuid;primaryKey;autoIncrement:false;comment:所属账号，普通账号清理时删除" json:"-"`
	ContentVersion int64           `gorm:"column:content_version;type:bigint;not null;comment:姓名内容版本，绑定认证加密附加数据" json:"-"`
	NameCipher     json.RawMessage `gorm:"column:name_cipher;type:jsonb;not null;comment:姓名，保存信封密文而非明文" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (IdentityName) TableName() string { return "public.identity_names" }

// IdentityReview 映射 public.identity_reviews，字段注释描述数据库持久化语义。
type IdentityReview struct {
	// DataCipher 按申请版本绑定待审姓名及学号，避免驳回材料污染已绑定身份。
	DataCipher         json.RawMessage `gorm:"column:data_cipher;type:jsonb;comment:按申请版本加密的待审姓名与学号，历史未提交材料为空" json:"-"`
	RevisionSuggestion *string         `gorm:"column:revision_suggestion;type:text;comment:驳回后的修改建议" json:"-"`
	ID                 string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	AccountID          string          `gorm:"column:account_id;type:uuid;not null;comment:申请账号" json:"-"`
	Version            int64           `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
	AssetID            string          `gorm:"column:asset_id;type:uuid;not null;comment:本次审核的学生卡对象" json:"-"`
	Status             string          `gorm:"column:status;type:text;not null;default:'pending';comment:审核状态" json:"-"`
	Reason             *string         `gorm:"column:reason;type:text;comment:驳回原因" json:"-"`
	ReviewerID         *string         `gorm:"column:reviewer_id;type:uuid;comment:处理审核的后台账号" json:"-"`
	ReviewedAt         *time.Time      `gorm:"column:reviewed_at;type:timestamptz;comment:完成审核时间" json:"-"`
	CreatedAt          time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (IdentityReview) TableName() string { return "public.identity_reviews" }

// Asset 映射 public.assets，字段注释描述数据库持久化语义。
type Asset struct {
	Version            int64           `gorm:"column:version;type:bigint;not null;default:1;comment:学生卡处理版本，用于锁后状态复核" json:"-"`
	ScanStatus         string          `gorm:"column:scan_status;type:text;not null;default:'pending';comment:图片安全状态，pending待处理、approved通过或rejected拒绝，不代表学生认证" json:"-"`
	ID                 string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	OwnerID            string          `gorm:"column:owner_id;type:uuid;not null;comment:普通账号所有者，不级联跳过外部对象清理" json:"-"`
	Purpose            string          `gorm:"column:purpose;type:text;not null;comment:普通对象仅学生卡，证据对象另存隔离域" json:"-"`
	EncryptedObjectKey json.RawMessage `gorm:"column:encrypted_object_key;type:jsonb;not null;comment:私有对象键，保存信封密文而非明文" json:"-"`
	ObjectVersion      string          `gorm:"column:object_version;type:text;not null;comment:外部对象版本，不使用公开下载地址" json:"-"`
	WrappedKeyRef      string          `gorm:"column:wrapped_key_ref;type:text;not null;comment:外部包裹密钥引用，不保存主密钥" json:"-"`
	Status             string          `gorm:"column:status;type:text;not null;default:'pending';comment:对象处理状态" json:"-"`
	CreatedAt          time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Asset) TableName() string { return "public.assets" }
