// Package command 提供数据库权威的短事务命令入口，不负责 HTTP 会话签发或具体业务规则。
package command

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// 稳定错误供 HTTP/消费者边界映射，不包含数据库、令牌或个人信息。
var (
	// ErrInvalid 表示请求或服务端策略无效。
	ErrInvalid = errors.New("命令输入无效")
	// ErrUnauthenticated 表示当前会话不可使用。
	ErrUnauthenticated = errors.New("会话无效")
	// ErrForbidden 表示当前权限不足。
	ErrForbidden = errors.New("当前权限不允许")
	// ErrNotFound 表示目标或结果不可访问。
	ErrNotFound = errors.New("目标不存在")
	// ErrVersionConflict 表示客户端版本已过期。
	ErrVersionConflict = errors.New("版本冲突")
	// ErrIdempotencyConflict 表示同一键关联不同请求。
	ErrIdempotencyConflict = errors.New("幂等键与请求不一致")
	// ErrAssetNotReady 表示本人资源仍在处理或未通过安全检查。
	ErrAssetNotReady = errors.New("资源尚未就绪")
	// ErrConflict 表示数据库业务约束冲突。
	ErrConflict = errors.New("业务约束冲突")
	// ErrActiveWishLimit 表示保留匹配的回退将超过活跃心愿额度。
	ErrActiveWishLimit = errors.New("活跃心愿额度已满")
	// ErrDuplicateApplication 表示同人同心愿已有未终结申请。
	ErrDuplicateApplication = errors.New("已有未终结申请")
	// ErrBlockedInteraction 表示双方当前存在拉黑限制。
	ErrBlockedInteraction = errors.New("当前不可交互")
	// ErrReviewPending 表示申请尚未获准生效。
	ErrReviewPending = errors.New("申请仍待审核")
	// ErrRetry 表示并发状态变化且达到上限后可再次请求。
	ErrRetry = errors.New("并发状态变化，请重试")
	// ErrUnavailable 表示数据库操作无法安全完成。
	ErrUnavailable = errors.New("命令存储不可用")
	// ErrInternal 表示回调失败或违反事务契约。
	ErrInternal = errors.New("命令执行失败")
)

// Config 由装配方显式确定技术超时、重试上限和回执保留期，不隐含生产参数。
type Config struct {
	Timeout     time.Duration
	LockTimeout time.Duration
	ReceiptTTL  time.Duration
	MaxAttempts int
}

// Access 是服务端路由固定的学生权限要求；Restricted 仅用于申诉、警告和隐私等限定入口。
type Access string

const (
	// Restricted 只要求有效会话与存续主体，允许受罚账号走限定操作。
	Restricted Access = "restricted"
	// Ordinary 拒绝当前禁言或封禁，不强求学生资格已通过。
	Ordinary Access = "ordinary"
	// Verified 额外要求当前学生资格通过。
	Verified Access = "verified"
)

// ResourceKind 对应固定白名单及锁序，不接收客户端表名。
type ResourceKind string

const (
	// Asset 是账号之后、互动资源之前锁定的私有学生卡。
	Asset ResourceKind = "asset"
	// IdentityReview 在学生卡之后锁定人工认证申请。
	IdentityReview ResourceKind = "identity_review"
	// Account 使用已经取得的账号 guard，不作为普通资源重复锁定。
	Account ResourceKind = "account"
	// Wish 在申请和会话前锁定。
	Wish ResourceKind = "wish"
	// Application 在心愿后锁定。
	Application ResourceKind = "application"
	// Match 在申请后锁定。
	Match ResourceKind = "match"
	// Conversation 在匹配后锁定。
	Conversation ResourceKind = "conversation"
	// ModerationTask 在会话后锁定，expected_version绑定案件独立版本。
	ModerationTask ResourceKind = "moderation_task"
	// Message 在审核任务后锁定。
	Message ResourceKind = "message"
	// Notification 在消息之后锁定通知。
	Notification ResourceKind = "notification"
)

// Resource 是需要按层级和 UUID 锁定的技术引用。
type Resource struct {
	Kind ResourceKind
	ID   string
}

// Expectation 将三个独立协议字段绑定到明确对象，禁止混用会话代次和聚合版本。
type Expectation struct {
	Name   string
	Target Resource
	Value  int64
}

// Discovery 是只读发现的完整参与集合，任一集合变化均回滚后重新发现。
type Discovery struct {
	Accounts  []string
	Admins    []string
	Resources []Resource
}

// Subject 是数据库会话解析出的主体引用，不接受客户端自行指定的身份。
type Subject struct {
	Kind string
	ID   string
}

// Actor 是持锁后从数据库读取的当前主体及后台角色。
type Actor struct {
	Subject
	AuthEpoch int64
	Roles     []string
}

// Input 保存临时请求输入；Request 须为处理器已验证的类型化 DTO，不持久化原文。
type Input struct {
	Token          string
	IdempotencyKey string
	RequestID      string
	Request        any
	Expectations   []Expectation
}

// Definition 只能由可信服务端装配，不能从请求反序列化策略或回调。
// ResourcePath 是已解析目标的规范资源路径，不含 query/fragment；回执按方法和该路径隔离。
// Discover 必须只读；Authorize 校验当前资格/对象访问，不能把首次业务状态当重放授权。
// Apply 仅执行本事务数据库操作，无外部发送；Resolve 重查当前结果可访问性，不回放正文。
type Definition struct {
	// IdentityLock 在主体锁之前串行化身份绑定与检索密钥轮换。
	IdentityLock bool
	Kind         string
	Method       string
	ResourcePath string
	ActorKind    string
	Access       Access
	AllowedRoles []string
	// RequiredExpectations 由路由声明必填并发字段，防止更新入口遗漏客户端版本。
	RequiredExpectations []string
	Discover             func(context.Context, *gorm.DB, Subject) (Discovery, error)
	Authorize            func(context.Context, *Tx) error
	Apply                func(context.Context, *Tx) (string, error)
	Resolve              func(context.Context, *Tx, string) error
}

// Outcome 只返回技术结果引用，不保留历史状态或正文投影。
type Outcome struct {
	Ref      string
	Replayed bool
	Attempts int
}

// Guard 是持锁账号的当前状态。
type Guard struct {
	ID           string
	Lifecycle    string
	Verification string
	AuthEpoch    int64
	Version      int64
}

// Snapshot 是已锁资源的版本元数据；零版本表示该表尚无通用版本字段，不可用作版本断言。
type Snapshot struct {
	Resource
	Version    int64
	WriteEpoch int64
}

// Tx 的生命周期由 Engine 独占；不得保存到回调之外、手动提交/回滚或建立独立写事务。
type Tx struct {
	db        *gorm.DB
	actor     Actor
	now       time.Time
	guards    map[string]Guard
	resources map[Resource]Snapshot
}

// DB 供 repository 使用当前事务，不能 Commit/Rollback 或关闭连接池。
func (t *Tx) DB() *gorm.DB { return t.db }

// Actor 返回当前授权主体的副本，修改返回值不会改变框架授权状态。
func (t *Tx) Actor() Actor {
	result := t.actor
	result.Roles = append([]string(nil), result.Roles...)
	return result
}

// Now 是取得全部锁后读取的数据库时间，本事务的业务期限判断复用该时间。
func (t *Tx) Now() time.Time { return t.now }

// Guard 返回已锁账号快照。
func (t *Tx) Guard(id string) (Guard, bool) { got, ok := t.guards[id]; return got, ok }

// Resource 返回已锁资源版本。
func (t *Tx) Resource(ref Resource) (Snapshot, bool) { got, ok := t.resources[ref]; return got, ok }
