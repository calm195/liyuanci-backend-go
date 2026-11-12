package wish

import "time"

// CommentInput 绑定当前心愿及评论代次，回复只接受同帖的技术引用。
type CommentInput struct {
	Body                  string  `json:"body"`
	Kind                  string  `json:"kind,omitempty"`
	ExpectedVersion       int64   `json:"expected_version,omitempty"`
	ExpectedPostVersion   int64   `json:"expected_post_version"`
	ExpectedCommentsEpoch int64   `json:"expected_comments_epoch"`
	RootCommentID         *string `json:"root_comment_id,omitempty"`
	ReplyToCommentID      *string `json:"reply_to_comment_id,omitempty"`
}

// CommentIdentity 不公开内部账号；匿名称呼限定在当前心愿上下文。
type CommentIdentity struct {
	Label    string `json:"label"`
	IsAuthor bool   `json:"is_author"`
}

// Comment 是本人状态或当前公开评论的安全投影；屏障下正文为空。
type Comment struct {
	ID                   string          `json:"id"`
	WishID               string          `json:"wish_id"`
	Version              int64           `json:"version"`
	ContentVersion       int64           `json:"content_version"`
	ParentContentVersion int64           `json:"parent_content_version"`
	Kind                 string          `json:"kind"`
	Body                 *string         `json:"body"`
	Status               string          `json:"status"`
	PublishedAt          *time.Time      `json:"published_at"`
	CreatedAt            time.Time       `json:"created_at"`
	RootCommentID        *string         `json:"root_comment_id"`
	ReplyToCommentID     *string         `json:"reply_to_comment_id"`
	ReplyToLabel         string          `json:"reply_to_label"`
	ReplyCount           int64           `json:"reply_count"`
	ViewerIsAuthor       bool            `json:"viewer_is_author"`
	Identity             CommentIdentity `json:"identity"`
	AllowedActions       []string        `json:"allowed_actions"`
	ReviewResult         *CommentReview  `json:"review_result,omitempty"`
}

// CommentReview 只返回当前正文版本的审核状态与驳回建议。
type CommentReview struct {
	Decision           string  `json:"decision"`
	Reason             *string `json:"reason,omitempty"`
	RevisionSuggestion *string `json:"revision_suggestion,omitempty"`
}

// CommentPage 使用有界游标，不将未公开内容混入公共列表。
type CommentPage struct {
	Items      []Comment `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

// CommentQuery 将根评论、回复及作者补充的游标分别隔离。
type CommentQuery struct {
	WishID, RootCommentID, Kind, Cursor string
	Limit                               int
}

// CommentSettings 是评论开关命令返回的当前权威状态。
type CommentSettings struct {
	Version       int64 `json:"version"`
	CommentsOpen  bool  `json:"comments_open"`
	CommentsEpoch int64 `json:"comments_epoch"`
}
