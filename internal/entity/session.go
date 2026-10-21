package entity

import (
	"time"
)

// Session 映射 public.sessions，字段注释描述数据库持久化语义。
type Session struct {
	ID          string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	AccountID   *string    `gorm:"column:account_id;type:uuid;comment:学生会话主体，与后台主体二选一" json:"-"`
	AdminID     *string    `gorm:"column:admin_id;type:uuid;comment:后台会话主体，与学生主体二选一" json:"-"`
	SubjectKind string     `gorm:"column:subject_kind;type:text;->;-:migration;comment:根据主体外键生成的会话命名空间" json:"-"`
	SubjectID   string     `gorm:"column:subject_id;type:uuid;->;-:migration;comment:根据主体外键生成的主体标识" json:"-"`
	TokenHash   []byte     `gorm:"column:token_hash;type:bytea;not null;unique;comment:会话令牌摘要，禁止存令牌原文" json:"-"`
	AuthEpoch   int64      `gorm:"column:auth_epoch;type:bigint;not null;comment:签发时主体权限代次，每次请求需与当前值复核" json:"-"`
	ExpiresAt   time.Time  `gorm:"column:expires_at;type:timestamptz;not null;comment:会话失效时间" json:"-"`
	RevokedAt   *time.Time `gorm:"column:revoked_at;type:timestamptz;comment:主动撤销时间" json:"-"`
	CreatedAt   time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Session) TableName() string { return "public.sessions" }

// StudentLoginExchange 映射 public.student_login_exchanges，字段注释描述数据库持久化语义。
type StudentLoginExchange struct {
	ID          string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机交换回执标识" json:"-"`
	CodeHash    []byte    `gorm:"column:code_hash;type:bytea;not null;unique;comment:用途隔离HMAC的微信code摘要，唯一限制一次交换" json:"-"`
	RequestHash []byte    `gorm:"column:request_hash;type:bytea;not null;comment:用途隔离HMAC的客户端请求标识摘要，防止跨请求重放" json:"-"`
	Nonce       []byte    `gorm:"column:nonce;type:bytea;not null;comment:独立随机会话派生材料，单独持有不能恢复令牌" json:"-"`
	Status      string    `gorm:"column:status;type:text;not null;default:'processing';comment:交换处理中或已原子提交身份及会话的完成状态" json:"-"`
	SessionID   *string   `gorm:"column:session_id;type:uuid;comment:完成后绑定的学生会话，随会话物理清理级联删除" json:"-"`
	ExpiresAt   time.Time `gorm:"column:expires_at;type:timestamptz;not null;comment:同code成功回执绝对有效期，到期必须重新获取code" json:"-"`
	CreatedAt   time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:数据库记录的领取时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (StudentLoginExchange) TableName() string { return "public.student_login_exchanges" }
