package entity

import (
	"encoding/json"
	"time"
)

// Wish 映射 public.wishes，字段注释描述数据库持久化语义。
type Wish struct {
	DraftDeletedAt        *time.Time `gorm:"column:draft_deleted_at;type:timestamptz;comment:草稿删除生效时间，保留技术引用但不再允许读取或复活" json:"-"`
	DurationDays          int16      `gorm:"column:duration_days;type:smallint;not null;default:7;comment:有效期整数天数，默认七天" json:"-"`
	ID                    string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	OwnerID               string     `gorm:"column:owner_id;type:uuid;not null;comment:许愿人" json:"-"`
	State                 string     `gorm:"column:state;type:text;not null;default:'draft';comment:心愿业务状态" json:"-"`
	CurrentContentVersion int64      `gorm:"column:current_content_version;type:bigint;not null;comment:当前内容版本，与聚合版本分离" json:"-"`
	Version               int64      `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
	Category              string     `gorm:"column:category;type:text;not null;comment:批准契约中的心愿分类" json:"-"`
	Campus                string     `gorm:"column:campus;type:text;not null;comment:批准契约中的校区范围" json:"-"`
	ExpiresAt             *time.Time `gorm:"column:expires_at;type:timestamptz;comment:当前权威到期时间，查询必须实时复核" json:"-"`
	RefreshOnPublish      bool       `gorm:"column:refresh_on_publish;type:boolean;not null;default:false;comment:下次上架是否按规则刷新到期时间" json:"-"`
	PublishedAt           *time.Time `gorm:"column:published_at;type:timestamptz;comment:最近上架时间，用于游标排序" json:"-"`
	RestrictionEpoch      int64      `gorm:"column:restriction_epoch;type:bigint;not null;default:1;comment:内容交互限制代次" json:"-"`
	CreatedAt             time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Wish) TableName() string { return "public.wishes" }

// WishVersion 映射 public.wish_versions，字段注释描述数据库持久化语义。
type WishVersion struct {
	ReviewTaskID           *string         `gorm:"column:review_task_id;type:uuid;comment:当前显式送审任务引用，旧任务不能覆盖本版本的新审核轮次" json:"-"`
	WishID                 string          `gorm:"column:wish_id;type:uuid;primaryKey;autoIncrement:false;comment:所属心愿" json:"-"`
	ContentVersion         int64           `gorm:"column:content_version;type:bigint;primaryKey;autoIncrement:false;comment:内容版本" json:"-"`
	BodyCipher             json.RawMessage `gorm:"column:body_cipher;type:jsonb;not null;comment:原始心愿正文，保存信封密文而非明文" json:"-"`
	PublicBody             *string         `gorm:"column:public_body;type:text;comment:审核脱敏正文，原始密文长度仍须服务层验证" json:"-"`
	IdentityTags           json.RawMessage `gorm:"column:identity_tags;type:jsonb;not null;default:'{}';comment:经审核允许的最小展示标签，不得包含完整身份" json:"-"`
	RewardMethod           string          `gorm:"column:reward_method;type:text;not null;default:'sincere_thanks';comment:报答方式" json:"-"`
	ModerationStatus       string          `gorm:"column:moderation_status;type:text;not null;default:'pending';comment:当前内容审核状态" json:"-"`
	WordlistVersion        *string         `gorm:"column:wordlist_version;type:uuid;comment:采用的词库发布版本引用" json:"-"`
	SchoolRelated          *bool           `gorm:"column:school_related;type:boolean;comment:涉校判定结果，未完成时为空而非默认否" json:"-"`
	SchoolDetectionVersion string          `gorm:"column:school_detection_version;type:text;not null;comment:涉校检测器版本" json:"-"`
	SchoolDetectionState   string          `gorm:"column:school_detection_state;type:text;not null;default:'pending';comment:涉校判定状态" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (WishVersion) TableName() string { return "public.wish_versions" }

// Application 映射 public.applications，字段注释描述数据库持久化语义。
type Application struct {
	ReviewTaskID        *string         `gorm:"column:review_task_id;type:uuid;comment:当前留言审核任务，旧审核不得恢复已撤销或失效申请" json:"-"`
	OwnerEpoch          *int64          `gorm:"column:owner_epoch;type:bigint;comment:申请时许愿人权限代次，历史空值不能作为生效授权" json:"-"`
	PublicMessageCipher json.RawMessage `gorm:"column:public_message_cipher;type:jsonb;comment:审核允许的脱敏留言密文，仅当前有效申请向许愿人解密" json:"-"`
	ID                  string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	WishID              string          `gorm:"column:wish_id;type:uuid;not null;comment:应愿目标心愿" json:"-"`
	WishContentVersion  int64           `gorm:"column:wish_content_version;type:bigint;not null;comment:申请时的目标心愿版本" json:"-"`
	ApplicantID         string          `gorm:"column:applicant_id;type:uuid;not null;comment:申请人" json:"-"`
	Status              string          `gorm:"column:status;type:text;not null;default:'pending_scan';comment:应愿状态" json:"-"`
	Version             int64           `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
	MessageCipher       json.RawMessage `gorm:"column:message_cipher;type:jsonb;not null;comment:应愿留言，保存信封密文而非明文" json:"-"`
	ContentVersion      int64           `gorm:"column:content_version;type:bigint;not null;default:1;comment:申请内容版本" json:"-"`
	CancelReason        *string         `gorm:"column:cancel_reason;type:text;comment:终结的稳定原因码，不存身份正文" json:"-"`
	CreatedAt           time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Application) TableName() string { return "public.applications" }

// Match 映射 public.matches，字段注释描述数据库持久化语义。
type Match struct {
	ApplicationID *string    `gorm:"column:application_id;type:uuid;comment:形成匹配的精确申请，历史空值不推断来源" json:"-"`
	Version       int64      `gorm:"column:version;type:bigint;not null;default:1;comment:匹配关系自身的乐观版本，不是心愿或申请版本" json:"-"`
	ID            string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	WishID        string     `gorm:"column:wish_id;type:uuid;not null;comment:对应心愿" json:"-"`
	RequesterID   string     `gorm:"column:requester_id;type:uuid;not null;comment:许愿人，与目标心愿作者一致" json:"-"`
	HelperID      string     `gorm:"column:helper_id;type:uuid;not null;comment:应愿人" json:"-"`
	Status        string     `gorm:"column:status;type:text;not null;default:'current';comment:匹配生命周期" json:"-"`
	StartedAt     time.Time  `gorm:"column:started_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:匹配开始时间" json:"-"`
	EndedAt       *time.Time `gorm:"column:ended_at;type:timestamptz;comment:匹配结束时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Match) TableName() string { return "public.matches" }

// Conversation 映射 public.conversations，字段注释描述数据库持久化语义。
type Conversation struct {
	Version         int64     `gorm:"column:version;not null;default:1;comment:会话聚合版本" json:"-"`
	ID              string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	MatchID         string    `gorm:"column:match_id;type:uuid;not null;unique;comment:唯一对应的匹配关系" json:"-"`
	WriteEpoch      int64     `gorm:"column:write_epoch;type:bigint;not null;default:1;comment:写权限代次，旧异步任务必须复核" json:"-"`
	NextDeliverySeq int64     `gorm:"column:next_delivery_seq;type:bigint;not null;default:1;comment:下一送达序号，仅在送达事务中更新" json:"-"`
	CreatedAt       time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Conversation) TableName() string { return "public.conversations" }

// Message 映射 public.messages，字段注释描述数据库持久化语义。
type Message struct {
	ReviewTaskID    *string         `gorm:"column:review_task_id;type:uuid;comment:当前审核任务" json:"-"`
	PeerEpoch       *int64          `gorm:"column:peer_epoch;comment:接收方权限代次" json:"-"`
	RequestDigest   []byte          `gorm:"column:request_digest;type:bytea;comment:客户端消息请求带密钥摘要" json:"-"`
	ID              string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	ConversationID  string          `gorm:"column:conversation_id;type:uuid;not null;comment:所属会话" json:"-"`
	SenderID        string          `gorm:"column:sender_id;type:uuid;not null;comment:发送者，是否为原参与者须命令复核" json:"-"`
	DeliverySeq     *int64          `gorm:"column:delivery_seq;type:bigint;comment:送达顺序号，未送达时为空" json:"-"`
	DeliveredAt     *time.Time      `gorm:"column:delivered_at;type:timestamptz;comment:最终送达时间，未送达时为空" json:"-"`
	Version         int64           `gorm:"column:version;type:bigint;not null;default:1;comment:聚合版本，用于期望版本校验" json:"-"`
	ClientMessageID string          `gorm:"column:client_message_id;type:text;not null;comment:客户端随机幂等标识" json:"-"`
	BodyCipher      json.RawMessage `gorm:"column:body_cipher;type:jsonb;not null;comment:消息正文，保存信封密文而非明文" json:"-"`
	ContentVersion  int64           `gorm:"column:content_version;type:bigint;not null;default:1;comment:消息内容版本" json:"-"`
	Status          string          `gorm:"column:status;type:text;not null;default:'pending_scan';comment:审核与送达状态" json:"-"`
	CancelEpoch     int64           `gorm:"column:cancel_epoch;type:bigint;not null;comment:提交时取消或写权限代次" json:"-"`
	CreatedAt       time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (Message) TableName() string { return "public.messages" }

// StoryVersion 映射 public.story_versions，字段注释描述数据库持久化语义。
type StoryVersion struct {
	WishID                 string          `gorm:"column:wish_id;type:uuid;primaryKey;autoIncrement:false;comment:所属心愿" json:"-"`
	StoryVersion           int64           `gorm:"column:story_version;type:bigint;primaryKey;autoIncrement:false;comment:故事内容版本" json:"-"`
	AuthorID               string          `gorm:"column:author_id;type:uuid;not null;comment:故事作者，须为许愿人" json:"-"`
	BodyCipher             json.RawMessage `gorm:"column:body_cipher;type:jsonb;not null;comment:原始故事正文，保存信封密文而非明文" json:"-"`
	PublicBody             *string         `gorm:"column:public_body;type:text;comment:经审核脱敏的故事公开投影" json:"-"`
	ModerationStatus       string          `gorm:"column:moderation_status;type:text;not null;default:'pending';comment:故事审核状态" json:"-"`
	SchoolRelated          *bool           `gorm:"column:school_related;type:boolean;comment:涉校判定结果，未完成时为空而非默认否" json:"-"`
	SchoolDetectionVersion string          `gorm:"column:school_detection_version;type:text;not null;comment:涉校检测器版本" json:"-"`
	SchoolDetectionState   string          `gorm:"column:school_detection_state;type:text;not null;default:'pending';comment:涉校判定状态" json:"-"`
	WithdrawnAt            *time.Time      `gorm:"column:withdrawn_at;type:timestamptz;comment:作者撤回时间" json:"-"`
	CreatedAt              time.Time       `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;autoCreateTime:false;autoUpdateTime:false;comment:创建时间，以带时区时间保存" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (StoryVersion) TableName() string { return "public.story_versions" }

// ContentRegistry 映射 public.content_registry，字段注释描述数据库持久化语义。
type ContentRegistry struct {
	ID                     string `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:随机对象标识" json:"-"`
	Kind                   string `gorm:"column:kind;type:text;not null;comment:内容类型" json:"-"`
	EntityID               string `gorm:"column:entity_id;type:uuid;not null;comment:内容实体标识，不以多态标识冒充外键" json:"-"`
	ContentVersion         int64  `gorm:"column:content_version;type:bigint;not null;comment:内容版本" json:"-"`
	AuthorID               string `gorm:"column:author_id;type:uuid;not null;comment:普通内容作者" json:"-"`
	Status                 string `gorm:"column:status;type:text;not null;default:'pending';comment:内容审核状态" json:"-"`
	SchoolRelated          *bool  `gorm:"column:school_related;type:boolean;comment:涉校判定结果，未完成时为空而非默认否" json:"-"`
	SchoolDetectionVersion string `gorm:"column:school_detection_version;type:text;not null;comment:涉校检测器版本" json:"-"`
	SchoolDetectionState   string `gorm:"column:school_detection_state;type:text;not null;default:'pending';comment:涉校判定状态" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (ContentRegistry) TableName() string { return "public.content_registry" }
