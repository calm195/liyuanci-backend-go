package moderation

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

const adminContentPageSize = 20

// AdminContentQuery 是内容管理的筛选项，日期范围为左闭右开。
type AdminContentQuery struct {
	Keyword, State, Requester, Helper string
	From, Until                       time.Time
	Cursor                            string `json:"-"`
}

// AdminContentSummary 是管理列表白名单。树洞记录始终省略账号关联。
type AdminContentSummary struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at"`
	Requester   string     `json:"requester"`
	Helper      string     `json:"helper"`
	MatchedAt   *time.Time `json:"matched_at"`
}

// AdminPublicationText 仅包含公开素材及其公开署名。
type AdminPublicationText struct {
	ID               string       `json:"id,omitempty"`
	Kind             string       `json:"kind,omitempty"`
	RootCommentID    *string      `json:"root_comment_id,omitempty"`
	ReplyToCommentID *string      `json:"reply_to_comment_id,omitempty"`
	PublishedAt      *time.Time   `json:"published_at,omitempty"`
	Images           []wish.Image `json:"images,omitempty"`
	Body             string       `json:"body"`
	Author           string       `json:"author"`
	Notice           string       `json:"notice"`
}

// AdminContentDetail 提供故事链路或分页评论以及当前可导出状态。
type AdminContentDetail struct {
	AdminContentSummary
	ApplicationCount int64                  `json:"application_count"`
	CommentCount     int64                  `json:"comment_count"`
	ViewCount        int64                  `json:"view_count"`
	ExpiresAt        *time.Time             `json:"expires_at"`
	FirstPublishedAt *time.Time             `json:"first_published_at"`
	Original         *AdminPublicationText  `json:"original"`
	Story            *AdminPublicationText  `json:"story"`
	Comments         []AdminPublicationText `json:"comments"`
	CommentsMore     bool                   `json:"comments_more"`
	Exportable       bool                   `json:"exportable"`
}

type contentPublication struct {
	title           string
	original, story *AdminPublicationText
}

// AdminContent 绑定经服务端授权的后台内容管理与来源模块的公开投影。
type AdminContent struct {
	review  *Service
	wishes  *wish.Service
	whisper *whisper.Service
}

// NewAdminWishContent 创建心愿管理入口。
func NewAdminWishContent(review *Service, wishes *wish.Service) (*AdminContent, error) {
	if review == nil || wishes == nil {
		return nil, command.ErrInvalid
	}
	return &AdminContent{review: review, wishes: wishes}, nil
}

// NewAdminWhisperContent 创建树洞管理入口。
func NewAdminWhisperContent(review *Service, whispers *whisper.Service) (*AdminContent, error) {
	if review == nil || whispers == nil {
		return nil, command.ErrInvalid
	}
	return &AdminContent{review: review, whisper: whispers}, nil
}

func (s *AdminContent) kind() string {
	if s.wishes != nil {
		return "wish"
	}
	return "whisper"
}

func (s *AdminContent) normalize(q *AdminContentQuery) error {
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.Requester = strings.TrimSpace(q.Requester)
	q.Helper = strings.TrimSpace(q.Helper)
	for _, value := range []string{q.Keyword, q.Requester, q.Helper} {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 80 || strings.ContainsRune(value, 0) {
			return command.ErrInvalid
		}
	}
	if !q.From.IsZero() && !q.Until.IsZero() && !q.From.Before(q.Until) {
		return command.ErrInvalid
	}
	states := []string{"", "draft", "pending_review", "rejected", "ready", "published", "matching", "realized", "fulfilled", "withdrawn", "expired"}
	if s.whisper != nil {
		if q.Requester != "" || q.Helper != "" {
			return command.ErrInvalid
		}
		states = []string{"", "draft", "scanning", "review_pending", "rejected", "published", "withdrawn", "cancelled", "isolated", "restricted"}
	}
	if !slices.Contains(states, q.State) {
		return command.ErrInvalid
	}
	return nil
}

func (s *AdminContent) publication(ctx context.Context, db *gorm.DB, now time.Time, rid, id string) (out contentPublication, err error) {
	if s.wishes != nil {
		original, story, e := s.wishes.ReadPublicationChain(ctx, db, now, rid, id)
		if e != nil {
			return out, e
		}
		out.title = original.Title
		for i := range original.Images {
			original.Images[i].URL = "/api/v1/admin/wish-images/" + original.Images[i].ID + "?wish_id=" + id
		}
		out.original = &AdminPublicationText{Body: original.Body, Notice: original.NoticeText, Images: original.Images}
		if story != nil {
			out.story = &AdminPublicationText{Body: story.Body, Notice: story.NoticeText, Images: story.Images}
		}
		return out, nil
	}
	post, e := s.whisper.ReadPublicationPost(ctx, db, now, id)
	if e != nil {
		return out, e
	}
	out.title = post.Title
	out.original = &AdminPublicationText{Body: post.Body, Author: post.Identity.Label}
	if post.NoticeText != nil {
		out.original.Notice = *post.NoticeText
	}
	return out, nil
}

func (s *AdminContent) exportable(p contentPublication) bool {
	return p.original != nil && (s.whisper != nil || p.story != nil)
}

// List 每次读取复核当前会话及岗位，搜索只匹配经过公开资格检查的投影。
func (s *AdminContent) List(ctx context.Context, token, rid string, q AdminContentQuery) (out CursorPage[AdminContentSummary], err error) {
	defer s.review.done("admin_content."+s.kind()+".list", rid, time.Now(), &err)
	if e := s.normalize(&q); e != nil {
		return out, e
	}
	err = s.review.transaction(ctx, func(db *gorm.DB) error {
		ctx := db.Statement.Context
		actor, e := s.review.authorize(ctx, db, token, command.Discovery{})
		if e != nil {
			return e
		}
		purpose := "admin_content." + s.kind() + "." + s.review.digest(q)
		after, e := s.review.after(actor, purpose, q.Cursor)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		out.Items = []AdminContentSummary{}
		const batchSize = 40
		const maxScan = 200
		for scanned := 0; scanned < maxScan; scanned += batchSize {
			rows, e := s.candidates(db, q, now, after, batchSize)
			if e != nil {
				return e
			}
			for _, row := range rows {
				p, e := s.publication(ctx, db, now, rid, row.ID)
				if e != nil && !errors.Is(e, command.ErrNotFound) {
					return e
				}
				if q.Keyword == "" || publicationMatches(p, q.Keyword) {
					if len(out.Items) == adminContentPageSize {
						next := s.review.cursor(actor, purpose, after)
						out.NextCursor = &next
						return nil
					}
					row.Title = p.title
					out.Items = append(out.Items, row)
				}
				after = row.ID
			}
			if len(rows) < batchSize {
				return nil
			}
		}
		next := s.review.cursor(actor, purpose, after)
		out.NextCursor = &next
		return nil
	})
	return
}

func publicationMatches(p contentPublication, keyword string) bool {
	keyword = strings.ToLower(keyword)
	return strings.Contains(strings.ToLower(p.title), keyword) ||
		p.original != nil && strings.Contains(strings.ToLower(p.original.Body), keyword) ||
		p.story != nil && strings.Contains(strings.ToLower(p.story.Body), keyword)
}

// Detail 返回后台元数据和经过公开资格检查的素材。评论页从零开始。
func (s *AdminContent) Detail(ctx context.Context, token, rid, id string, page int) (out AdminContentDetail, err error) {
	defer s.review.done("admin_content."+s.kind()+".detail", rid, time.Now(), &err)
	if !validID(id) || page < 0 || page >= 1000 {
		return out, command.ErrInvalid
	}
	err = s.review.transaction(ctx, func(db *gorm.DB) error {
		ctx := db.Statement.Context
		if _, e := s.review.authorize(ctx, db, token, command.Discovery{}); e != nil {
			return e
		}
		now := time.Now().UTC()
		row, e := s.summary(db, id, now)
		if e != nil {
			return e
		}
		p, e := s.publication(ctx, db, now, rid, id)
		if e != nil && !errors.Is(e, command.ErrNotFound) {
			return e
		}
		row.Title = p.title
		out = AdminContentDetail{
			AdminContentSummary: row,
			Original:            p.original,
			Story:               p.story,
			Comments:            []AdminPublicationText{},
			Exportable:          s.exportable(p),
		}
		if s.wishes != nil {
			metadata, e := s.wishes.ReadAdminMetadata(ctx, db, now, id)
			if e != nil {
				return e
			}
			out.ApplicationCount = metadata.ApplicationCount
			out.CommentCount = metadata.CommentCount
			out.ViewCount = metadata.ViewCount
			out.ExpiresAt = metadata.ExpiresAt
			out.FirstPublishedAt = metadata.FirstPublishedAt
		}
		if p.original != nil {
			const commentsPageSize = 30
			out.Comments, out.CommentsMore, e = s.comments(ctx, db, now, id, page*commentsPageSize, commentsPageSize)
			return e
		}
		return nil
	})
	return
}

func (s *AdminContent) comments(ctx context.Context, db *gorm.DB, now time.Time, id string, offset, limit int) (items []AdminPublicationText, more bool, err error) {
	if s.wishes != nil {
		rows, more, e := s.wishes.ReadPublicationComments(ctx, db, now, id, offset, limit)
		if e != nil {
			return nil, false, e
		}
		items = make([]AdminPublicationText, 0, len(rows))
		for _, row := range rows {
			if row.Body != nil {
				items = append(items, AdminPublicationText{ID: row.ID, Kind: row.Kind, RootCommentID: row.RootCommentID, ReplyToCommentID: row.ReplyToCommentID, PublishedAt: row.PublishedAt, Body: *row.Body, Author: row.Identity.Label})
			}
		}
		return items, more, nil
	}
	rows, more, e := s.whisper.ReadPublicationComments(ctx, db, now, id, offset, limit)
	if e != nil {
		return nil, false, e
	}
	items = make([]AdminPublicationText, 0, len(rows))
	for _, row := range rows {
		item := AdminPublicationText{ID: row.ID, Kind: "comment", RootCommentID: row.RootCommentID, ReplyToCommentID: row.ReplyToCommentID, Body: row.Body, Author: row.Identity.Label}
		if row.NoticeText != nil {
			item.Notice = *row.NoticeText
		}
		items = append(items, item)
	}
	return items, more, nil
}
