package entity

import (
	"encoding/json"
	"time"
)

// Account 映射 public.accounts，字段注释描述数据库持久化语义。
type Account struct {
	ID               string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机账号标识，同时用于统一锁序" json:"-"`
	Lifecycle        string    `gorm:"column:lifecycle;type:text;not null;default:'active';comment:账号生命周期：active 正常，deleting 注销处理中" json:"-"`
	Verification     string    `gorm:"column:verification;type:text;not null;default:'unverified';comment:学生资格审核状态，身份绑定本身不代表审核通过" json:"-"`
	AuthEpoch        int64     `gorm:"column:auth_epoch;type:bigint;not null;default:1;comment:权限代次，变更后用于撤销旧会话授权" json:"-"`
	Version          int64     `gorm:"column:version;type:bigint;not null;default:1;comment:聚合状态版本，用于命令期望版本校验" json:"-"`
	Nickname         string    `gorm:"column:nickname;type:text;not null;comment:用户昵称，不得为空白" json:"-"`
	HideIdentityTags bool      `gorm:"column:hide_identity_tags;type:boolean;not null;default:false;comment:是否隐藏公开身份标签" json:"-"`
	CreatedAt        time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:账号创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Account) TableName() string { return "public.accounts" }

// Block 映射 public.blocks，字段注释描述数据库持久化语义。
type Block struct {
	OwnerID   string    `gorm:"column:owner_id;type:uuid;primaryKey;autoIncrement:false;comment:设置拉黑的账号" json:"-"`
	TargetID  string    `gorm:"column:target_id;type:uuid;primaryKey;autoIncrement:false;comment:被拉黑的账号" json:"-"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Block) TableName() string { return "public.blocks" }

// Penalty 映射 public.penalties，字段注释描述数据库持久化语义。
type Penalty struct {
	ID               string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	AccountID        string     `gorm:"column:account_id;type:uuid;not null;comment:受处置账号" json:"-"`
	Level            string     `gorm:"column:level;type:text;not null;comment:违规等级" json:"-"`
	ActionKind       string     `gorm:"column:action_kind;type:text;not null;comment:处置动作，warning 不限制交互" json:"-"`
	ViolationRef     string     `gorm:"column:violation_ref;type:uuid;not null;comment:违规事件引用，不直接依赖证据主体存续" json:"-"`
	ContentVersion   int64      `gorm:"column:content_version;type:bigint;not null;comment:触发处置的精确内容版本" json:"-"`
	StartsAt         time.Time  `gorm:"column:starts_at;type:timestamptz;not null;comment:处置开始时间" json:"-"`
	EndsAt           *time.Time `gorm:"column:ends_at;type:timestamptz;comment:七天禁言的结束时间，警告和永久封禁为空" json:"-"`
	ReasonRef        string     `gorm:"column:reason_ref;type:uuid;not null;comment:处置原因技术引用，不复制正文" json:"-"`
	FirstDisplayedAt *time.Time `gorm:"column:first_displayed_at;type:timestamptz;comment:首次警告展示时间，后续重进不得重置" json:"-"`
	ConfirmableAt    *time.Time `gorm:"column:confirmable_at;type:timestamptz;comment:首次展示三十秒后的可确认时间" json:"-"`
	AcknowledgedAt   *time.Time `gorm:"column:acknowledged_at;type:timestamptz;comment:本人确认警告时间，不得提前" json:"-"`
	Version          int64      `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
	RevokedAt        *time.Time `gorm:"column:revoked_at;type:timestamptz;comment:经授权撤销时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Penalty) TableName() string { return "public.penalties" }

// Appeal 映射 public.appeals，字段注释描述数据库持久化语义。
type Appeal struct {
	ID             string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	AccountID      string          `gorm:"column:account_id;type:uuid;not null;comment:申诉账号" json:"-"`
	PenaltyID      string          `gorm:"column:penalty_id;type:uuid;not null;comment:申诉目标处置，主体一致性由命令复核" json:"-"`
	BodyCipher     json.RawMessage `gorm:"column:body_cipher;type:jsonb;not null;comment:申诉正文，保存信封密文而非明文" json:"-"`
	Status         string          `gorm:"column:status;type:text;not null;default:'pending';comment:申诉处理状态" json:"-"`
	HandlerID      *string         `gorm:"column:handler_id;type:uuid;comment:处理人" json:"-"`
	DecisionReason *string         `gorm:"column:decision_reason;type:text;comment:处置结论理由" json:"-"`
	CreatedAt      time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Appeal) TableName() string { return "public.appeals" }
