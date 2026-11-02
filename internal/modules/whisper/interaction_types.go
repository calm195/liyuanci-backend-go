package whisper

// CommentInput 绑定当前父帖版本、评论代次与本人评论版本。
type CommentInput struct {
	ContentInput
	ExpectedVersion       int64 `json:"expected_version,omitempty"`
	ExpectedPostVersion   int64 `json:"expected_post_version"`
	ExpectedCommentsEpoch int64 `json:"expected_comments_epoch"`
}

// OwnerComment 仅向本人提供当前合法原稿，屏障与失效父上下文优先。
type OwnerComment struct {
	ID                   string         `json:"id"`
	Version              int64          `json:"version"`
	ContentVersion       int64          `json:"content_version"`
	Status               string         `json:"status"`
	Body                 *string        `json:"body"`
	IdentityInput        *IdentityInput `json:"identity_input"`
	ReviewResult         *ReviewResult  `json:"review_result"`
	CreatedAt            string         `json:"created_at"`
	UpdatedAt            string         `json:"updated_at"`
	PublishedAt          *string        `json:"published_at"`
	AllowedActions       []string       `json:"allowed_actions"`
	PostID               *string        `json:"post_id"`
	ParentContentVersion int64          `json:"parent_content_version"`
	RecoveryBody         *string        `json:"recovery_body"`
}

// PublicComment 不包含作者账号或可跨帖关联的内部标识。
type PublicComment struct {
	ID                   string         `json:"id"`
	Version              int64          `json:"version"`
	ContentVersion       int64          `json:"content_version"`
	Body                 string         `json:"body"`
	Identity             PublicIdentity `json:"identity"`
	PublishedAt          string         `json:"published_at"`
	SchoolRelated        bool           `json:"school_related"`
	NoticeCodes          []string       `json:"notice_codes"`
	NoticeText           *string        `json:"notice_text"`
	ViewerIsAuthor       bool           `json:"viewer_is_author"`
	AllowedActions       []string       `json:"allowed_actions"`
	PostID               string         `json:"post_id"`
	ParentContentVersion int64          `json:"parent_content_version"`
}

// HugInput 使用独立抱抱版本，不从总数推断个人状态。
type HugInput struct {
	ExpectedPostVersion int64 `json:"expected_post_version"`
	ExpectedHugVersion  int64 `json:"expected_hug_version"`
	Active              bool  `json:"active"`
}

// HugResult 返回当前权威计数与本人状态。
type HugResult struct {
	PostID   string   `json:"post_id"`
	HugCount int64    `json:"hug_count"`
	MyHug    HugState `json:"my_hug"`
}

// Notification 只保存技术跳转引用，回流必须重新请求当前目标。
type Notification struct {
	ID             string   `json:"id"`
	Version        int64    `json:"version"`
	Kind           string   `json:"kind"`
	TargetKind     string   `json:"target_kind"`
	TargetID       string   `json:"target_id"`
	CommentID      *string  `json:"comment_id"`
	Read           bool     `json:"read"`
	CreatedAt      string   `json:"created_at"`
	Notice         string   `json:"notice"`
	AllowedActions []string `json:"allowed_actions"`
}
