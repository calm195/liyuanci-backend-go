package entity

import (
	"encoding/json"
	"time"
)

// WorkItem 映射 public.work_items，字段注释描述数据库持久化语义。
type WorkItem struct {
	ID          string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	Kind        string     `gorm:"column:kind;type:text;not null;comment:受控工作类型" json:"-"`
	TargetKind  string     `gorm:"column:target_kind;type:text;not null;comment:技术目标类型" json:"-"`
	TargetID    string     `gorm:"column:target_id;type:uuid;not null;comment:技术目标随机标识" json:"-"`
	Version     int64      `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
	OperationID string     `gorm:"column:operation_id;type:uuid;not null;unique;comment:跨重试稳定操作标识" json:"-"`
	Status      string     `gorm:"column:status;type:text;not null;default:'pending';comment:工作状态，异常待办不能绕过取消" json:"-"`
	LeaseUntil  *time.Time `gorm:"column:lease_until;type:timestamptz;comment:当前处理租约期限" json:"-"`
	Attempts    int32      `gorm:"column:attempts;type:integer;not null;default:0;comment:已尝试次数" json:"-"`
	NextAt      time.Time  `gorm:"column:next_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:最早可再次执行时间" json:"-"`
	CreatedAt   time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (WorkItem) TableName() string { return "public.work_items" }

// OutboxEvent 映射 public.outbox_events，字段注释描述数据库持久化语义。
type OutboxEvent struct {
	EventID          string     `gorm:"column:event_id;type:uuid;primaryKey;autoIncrement:false;comment:随机事件标识" json:"-"`
	WorkID           string     `gorm:"column:work_id;type:uuid;not null;comment:持久工作源，消费不得从事件重建取消工作" json:"-"`
	AggregateID      string     `gorm:"column:aggregate_id;type:uuid;not null;comment:聚合随机技术标识" json:"-"`
	AggregateVersion int64      `gorm:"column:aggregate_version;type:bigint;not null;comment:事件关联的聚合版本" json:"-"`
	EventType        string     `gorm:"column:event_type;type:text;not null;comment:稳定事件类型" json:"-"`
	CreatedAt        time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
	LeaseUntil       *time.Time `gorm:"column:lease_until;type:timestamptz;comment:转发租约期限" json:"-"`
	SentAt           *time.Time `gorm:"column:sent_at;type:timestamptz;comment:已确认发送时间" json:"-"`
	NextAt           time.Time  `gorm:"column:next_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:最早重试时间" json:"-"`
	Attempts         int32      `gorm:"column:attempts;type:integer;not null;default:0;comment:转发尝试次数" json:"-"`
	SupersededAt     *time.Time `gorm:"column:superseded_at;type:timestamptz;comment:被已授权重驱动替代的技术时间，非空后禁止领取、确认和消费旧唤醒" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (OutboxEvent) TableName() string { return "public.outbox_events" }

// ConsumerReceipt 映射 public.consumer_receipts，字段注释描述数据库持久化语义。
type ConsumerReceipt struct {
	ConsumerKind string    `gorm:"column:consumer_kind;type:text;primaryKey;autoIncrement:false;comment:受控消费者类型" json:"-"`
	OperationID  string    `gorm:"column:operation_id;type:uuid;primaryKey;autoIncrement:false;comment:稳定工作操作标识" json:"-"`
	CompletedAt  time.Time `gorm:"column:completed_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:业务结果完成时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (ConsumerReceipt) TableName() string { return "public.consumer_receipts" }

// Notification 映射 public.notifications，字段注释描述数据库持久化语义。
type Notification struct {
	Version     int64      `gorm:"column:version;not null;default:1;comment:通知已读乐观版本" json:"-"`
	ID          string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	RecipientID string     `gorm:"column:recipient_id;type:uuid;not null;comment:通知接收人" json:"-"`
	Type        string     `gorm:"column:type;type:text;not null;comment:通用通知类型" json:"-"`
	TargetRef   string     `gorm:"column:target_ref;type:uuid;not null;comment:目标随机引用，访问时重新验证授权" json:"-"`
	ReadAt      *time.Time `gorm:"column:read_at;type:timestamptz;comment:本人阅读时间" json:"-"`
	CreatedAt   time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Notification) TableName() string { return "public.notifications" }

// DeliveryAttempt 映射 public.delivery_attempts，字段注释描述数据库持久化语义。
type DeliveryAttempt struct {
	ID             string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	NotificationID string    `gorm:"column:notification_id;type:uuid;not null;comment:对应站内通知" json:"-"`
	Channel        string    `gorm:"column:channel;type:text;not null;comment:投递渠道" json:"-"`
	Status         string    `gorm:"column:status;type:text;not null;default:'pending';comment:单次投递状态" json:"-"`
	AttemptedAt    time.Time `gorm:"column:attempted_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:尝试时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (DeliveryAttempt) TableName() string { return "public.delivery_attempts" }

// CommandReceipt 映射 public.command_receipts，字段注释描述数据库持久化语义。
type CommandReceipt struct {
	LifecycleBound bool      `gorm:"column:lifecycle_bound;not null;default:false;comment:回执绑定业务幂等生命周期，技术到期后仍重验当前访问，不覆盖或按期清理" json:"-"`
	ActorKind      string    `gorm:"column:actor_kind;type:text;primaryKey;autoIncrement:false;comment:调用者命名空间" json:"-"`
	ActorID        string    `gorm:"column:actor_id;type:uuid;primaryKey;autoIncrement:false;comment:调用者技术标识，多态主体存在性由命令校验" json:"-"`
	CommandKind    string    `gorm:"column:command_kind;type:text;primaryKey;autoIncrement:false;comment:受控路由命令类型" json:"-"`
	KeyHash        []byte    `gorm:"column:key_hash;type:bytea;primaryKey;autoIncrement:false;comment:幂等键摘要" json:"-"`
	RequestHash    []byte    `gorm:"column:request_hash;type:bytea;not null;comment:规范请求摘要，不保存原始请求" json:"-"`
	ResultRef      string    `gorm:"column:result_ref;type:uuid;not null;comment:命令结果技术引用" json:"-"`
	ExpiresAt      time.Time `gorm:"column:expires_at;type:timestamptz;not null;comment:回执清理期限" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (CommandReceipt) TableName() string { return "public.command_receipts" }

// AccessAudit 映射 public.access_audits，字段注释描述数据库持久化语义。
type AccessAudit struct {
	ID         string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	ActorID    string    `gorm:"column:actor_id;type:uuid;not null;comment:访问后台账号" json:"-"`
	SubjectRef string    `gorm:"column:subject_ref;type:uuid;not null;comment:被访问普通主体技术引用，注销清理须显式处理" json:"-"`
	Purpose    string    `gorm:"column:purpose;type:text;not null;comment:授权访问目的" json:"-"`
	At         time.Time `gorm:"column:at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:访问时间" json:"-"`
	Outcome    string    `gorm:"column:outcome;type:text;not null;comment:访问结果" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (AccessAudit) TableName() string { return "public.access_audits" }

// DeletionJob 映射 public.deletion_jobs，字段注释描述数据库持久化语义。
type DeletionJob struct {
	Scope           string          `gorm:"column:scope;type:text;not null;default:account;comment:删除范围，普通账号或独立证据" json:"-"`
	TargetRef       *string         `gorm:"column:target_ref;type:uuid;comment:清理期间技术目标，完成后清空，不绑定普通身份外键" json:"-"`
	ExpiresAt       time.Time       `gorm:"column:expires_at;type:timestamptz;not null;default:(CURRENT_TIMESTAMP + interval '7 days');comment:匿名回执绝对失效时间，清理工作不因回执过期停止" json:"-"`
	NextAt          time.Time       `gorm:"column:next_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:下一次清理时间，失败继续持久重试" json:"-"`
	Attempts        int             `gorm:"column:attempts;not null;default:0;comment:清理尝试次数，不保存底层错误" json:"-"`
	JobID           string          `gorm:"column:job_id;type:uuid;primaryKey;autoIncrement:false;comment:随机删除回执句柄" json:"-"`
	SubjectRef      *string         `gorm:"column:subject_ref;type:uuid;comment:工作期间主体关联，完成后必须清空" json:"-"`
	Phase           string          `gorm:"column:phase;type:text;not null;default:'intent';comment:清理阶段" json:"-"`
	RemainingCounts json.RawMessage `gorm:"column:remaining_counts;type:jsonb;not null;default:'{}';comment:受控类别剩余数量，不存正文或身份" json:"-"`
	ErrorCode       *string         `gorm:"column:error_code;type:text;comment:安全错误码，不存底层异常原文" json:"-"`
	CreatedAt       time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (DeletionJob) TableName() string { return "public.deletion_jobs" }
