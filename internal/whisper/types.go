// Package whisper 提供独立文字树洞领域；生产装配须先完成审核、危机与隐私保护。
package whisper

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// Tags 是用户显式选择的受控标签，不推断学生资料。
type Tags struct {
	Campus string `json:"campus"`
	Grade  string `json:"grade"`
	Gender string `json:"gender"`
}

// IdentityInput 三种互斥档位由 HTTP 与领域共同校验。
type IdentityInput struct {
	Mode              string `json:"mode"`
	Tags              *Tags  `json:"tags,omitempty"`
	DictionaryVersion *int64 `json:"dictionary_version,omitempty"`
	NicknameVersion   *int64 `json:"nickname_version,omitempty"`
}

// ContentInput 保留原文空白，只限制码点与纯空白。
type ContentInput struct {
	Body     string        `json:"body"`
	Identity IdentityInput `json:"identity"`
}

// PostInput 用于创建、完整替换、提交或撤回；具体路由决定有效字段。
type PostInput struct {
	ContentInput
	ExpectedVersion int64 `json:"expected_version,omitempty"`
}

// NicknameInput 的版本来自昵称偏好，不能以账号版本替代。
type NicknameInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Label           string `json:"label"`
}

// ReviewResult 仅描述精确正文版本的本轮审核结论。
type ReviewResult struct {
	Decision           string  `json:"decision"`
	ContentVersion     int64   `json:"content_version"`
	Reason             *string `json:"reason"`
	RevisionSuggestion *string `json:"revision_suggestion"`
}

// OwnerPost 与公开投影独立，屏障优先遮蔽原稿。
type OwnerPost struct {
	ID             string         `json:"id"`
	Version        int64          `json:"version"`
	ContentVersion int64          `json:"content_version"`
	Status         string         `json:"status"`
	Body           *string        `json:"body"`
	IdentityInput  *IdentityInput `json:"identity_input"`
	ReviewResult   *ReviewResult  `json:"review_result"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
	PublishedAt    *string        `json:"published_at"`
	AllowedActions []string       `json:"allowed_actions"`
	CommentsOpen   bool           `json:"comments_open"`
	CommentsEpoch  int64          `json:"comments_epoch"`
}

// PublicIdentity 不包含账号、别名行或昵称版本的可反查引用。
type PublicIdentity struct {
	Mode     string `json:"mode"`
	Label    string `json:"label"`
	Tags     *Tags  `json:"tags"`
	IsAuthor bool   `json:"is_author"`
}

// HugState 是本人抱抱目标态，不输出互动者列表。
type HugState struct {
	Active  bool  `json:"active"`
	Version int64 `json:"version"`
}

// PublicPost 只返回当前已批准版本及同步安全投影。
type PublicPost struct {
	ID             string         `json:"id"`
	Version        int64          `json:"version"`
	ContentVersion int64          `json:"content_version"`
	Body           string         `json:"body"`
	Identity       PublicIdentity `json:"identity"`
	PublishedAt    string         `json:"published_at"`
	SchoolRelated  bool           `json:"school_related"`
	NoticeCodes    []string       `json:"notice_codes"`
	NoticeText     *string        `json:"notice_text"`
	ViewerIsAuthor bool           `json:"viewer_is_author"`
	AllowedActions []string       `json:"allowed_actions"`
	CommentsOpen   bool           `json:"comments_open"`
	CommentsEpoch  int64          `json:"comments_epoch"`
	CommentCount   int64          `json:"comment_count"`
	HugCount       int64          `json:"hug_count"`
	MyHug          *HugState      `json:"my_hug"`
}

// Page 保留有界扫描后可能为空且仍有游标的页面。
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// Query 只允许固定的时间与随机标识分页，不支持作者检索。
type Query struct {
	Limit  int
	Cursor string
}

// NicknameLabel 表示当前可选已批准版本。
type NicknameLabel struct {
	NicknameVersion int64  `json:"nickname_version"`
	Label           string `json:"label"`
}

// NicknameCandidate 不会替换旧 active，直至审核最终批准。
type NicknameCandidate struct {
	NicknameLabel
	Status       string        `json:"status"`
	ReviewResult *ReviewResult `json:"review_result"`
}

// NicknameState 的版本与昵称治理版本互相独立。
type NicknameState struct {
	Version        int64              `json:"version"`
	Active         *NicknameLabel     `json:"active"`
	Candidate      *NicknameCandidate `json:"candidate"`
	AllowedActions []string           `json:"allowed_actions"`
}

// Item 是经资料方核验的受控代码或展示资料。
type Item struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// HelpItem 不提供默认或示例热线。
type HelpItem struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Contact   string `json:"contact"`
	SourceURL string `json:"source_url"`
}

// HelpDirectory 只有核验信息完整时才能 ready。
type HelpDirectory struct {
	Status     string     `json:"status"`
	Version    *int64     `json:"version"`
	VerifiedAt *string    `json:"verified_at"`
	Items      []HelpItem `json:"items"`
}

// Dictionary 的版本变化即时影响新输入；历史标签只按当前值有效性降级。
type Dictionary struct {
	Version *int64 `json:"version"`
	Items   []Item `json:"items"`
}

// CatalogSnapshot 由资料适配器原子返回，不包含个人资料。
type CatalogSnapshot struct {
	Grades  Dictionary
	Reports []Item
	Help    HelpDirectory
}

// Catalog 每次投影读取当前资料，缓存失效由适配器负责。
type Catalog interface {
	Load(context.Context) (CatalogSnapshot, error)
}

// Limits 固定产品边界，不允许用运行预算改变 Wire。
type Limits struct {
	TextCodepoints         int `json:"text_codepoints"`
	NicknameMin            int `json:"nickname_min"`
	NicknameMax            int `json:"nickname_max"`
	CapsuleMinDelaySeconds int `json:"capsule_min_delay_seconds"`
	CapsuleMaxDelaySeconds int `json:"capsule_max_delay_seconds"`
	CapsuleActiveLimit     int `json:"capsule_active_limit"`
}

// ModuleConfig 关闭时仍可查询配置和援助资料。
type ModuleConfig struct {
	ContractVersion string     `json:"contract_version"`
	Enabled         bool       `json:"enabled"`
	ServerNow       string     `json:"server_now"`
	SemiAvailable   bool       `json:"semi_available"`
	GradeDictionary Dictionary `json:"grade_dictionary"`
	Limits          Limits     `json:"limits"`
	ReportTypes     []Item     `json:"report_types"`
	BlockAPI        string     `json:"block_api"`
}

// Workspace 只展示本阶段实际可用的能力，动作提示不替代写入授权。
type Workspace struct {
	ServerNow       string   `json:"server_now"`
	Verification    string   `json:"verification"`
	WriteRestricted bool     `json:"write_restricted"`
	AllowedActions  []string `json:"allowed_actions"`
}

// ReviewSubmission 是同事务送审技术上下文，不传明文或账号至 MQ。
type ReviewSubmission struct {
	TaskID         string
	ContentID      string
	Kind           string
	ContentVersion int64
	Round          int64
}

// ReviewSink 仅在当前数据库事务内追加持久工作，不发外部消息或提交事务。
// 未注入完成新类型适配的实现时，提交与昵称设置失败关闭。
type ReviewSink interface {
	Append(context.Context, *gorm.DB, ReviewSubmission) error
}

// Admission 控制 Redis 滚动预算；受命令回执保护的重放不再次占用预算。
// Reserve 返回独立租约引用；Release 只撤销已确认失败的新命令预留。
type Admission interface {
	Reserve(context.Context, string, string) (string, error)
	Release(context.Context, string) error
}

// Config 显式注入开关、期限与独立用途密钥；不拥有依赖生命周期。
type Config struct {
	Enabled    bool
	Command    command.Config
	ReceiptKey []byte
	CursorKey  []byte
}

func timestamp(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }
