package entity

import (
	"time"
)

// AdminAccount 映射 public.admin_accounts，字段注释描述数据库持久化语义。
type AdminAccount struct {
	ID            string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	CredentialRef *string   `gorm:"column:credential_ref;type:text;unique;comment:外部后台身份引用，供应商交付前不填入模拟账号" json:"-"`
	PasswordHash  *string   `gorm:"column:password_hash;type:text;comment:可选本地凭据摘要，不能保存明文口令" json:"-"`
	Status        string    `gorm:"column:status;type:text;not null;default:'active';comment:后台账号状态" json:"-"`
	AuthEpoch     int64     `gorm:"column:auth_epoch;type:bigint;not null;default:1;comment:后台权限代次，撤销旧会话使用" json:"-"`
	Version       int64     `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
	CreatedAt     time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
	LoginName     *string   `gorm:"column:login_name;type:text;comment:本地后台登录名，精确匹配且唯一，历史账号不自动补名" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (AdminAccount) TableName() string { return "public.admin_accounts" }

// AdminRoleBinding 映射 public.admin_role_bindings，字段注释描述数据库持久化语义。
type AdminRoleBinding struct {
	AdminID string `gorm:"column:admin_id;type:uuid;primaryKey;autoIncrement:false;comment:后台账号" json:"-"`
	Role    string `gorm:"column:role;type:text;primaryKey;autoIncrement:false;comment:应用角色，不等同于数据库权限组" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (AdminRoleBinding) TableName() string { return "public.admin_role_bindings" }

// AdminAccountAction 映射 public.admin_account_actions，字段注释描述数据库持久化语义。
type AdminAccountAction struct {
	ID         string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:操作随机标识" json:"-"`
	ActorID    string    `gorm:"column:actor_id;type:uuid;not null;comment:执行操作的后台账号，初始超管初始化时为自身" json:"-"`
	TargetID   string    `gorm:"column:target_id;type:uuid;not null;comment:本次操作的后台目标账号" json:"-"`
	ActionKind string    `gorm:"column:action_kind;type:text;not null;comment:后台初始化、创建、本人改密或超管角色修改动作" json:"-"`
	Reason     string    `gorm:"column:reason;type:text;not null;comment:创建理由或服务端固定的本人改密用途，不接收口令" json:"-"`
	At         time.Time `gorm:"column:at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:事务记录时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (AdminAccountAction) TableName() string { return "public.admin_account_actions" }
