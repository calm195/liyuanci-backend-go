package wish

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func (s *Service) projectComment(ctx context.Context, db *gorm.DB, id, actor string, own bool, now time.Time) (out Comment, err error) {
	c, v, e := loadWishComment(ctx, db, id)
	if e != nil {
		return out, e
	}
	if own && c.AuthorID != actor {
		return out, command.ErrNotFound
	}
	out = Comment{ID: id, WishID: c.WishID, Version: c.Version, ContentVersion: c.ContentVersion, ParentContentVersion: c.ParentContentVersion, Kind: c.Kind, Status: c.Status, PublishedAt: c.PublishedAt, CreatedAt: c.CreatedAt, RootCommentID: c.RootCommentID, ReplyToCommentID: c.ReplyToCommentID, ViewerIsAuthor: actor == c.AuthorID, AllowedActions: []string{}}
	var w entity.Wish
	if e = db.Where("id=?", c.WishID).Take(&w).Error; e != nil {
		return Comment{}, command.ErrNotFound
	}
	var blocked int64
	if e = db.Raw(`SELECT (SELECT count(*) FROM public.accounts WHERE id IN(?,?) AND lifecycle<>'active')+(SELECT count(*) FROM public.privacy_holds h WHERE h.status='active' AND ((h.target_kind='wish' AND h.target_id=?) OR(h.target_kind='wish_comment' AND h.target_id IN(?,?,?))))+(SELECT count(*) FROM public.content_restrictions x JOIN public.content_registry r ON r.id=x.content_id WHERE x.active AND ((r.kind='wish' AND r.entity_id=?) OR(r.kind='wish_comment' AND r.entity_id IN(?,?,?))))`, w.OwnerID, c.AuthorID, w.ID, c.ID, c.RootCommentID, c.ReplyToCommentID, w.ID, c.ID, c.RootCommentID, c.ReplyToCommentID).Scan(&blocked).Error; e != nil {
		return Comment{}, e
	}
	if blocked > 0 || w.DraftDeletedAt != nil {
		if own {
			out.Status = "privacy_hold"
			out.AllowedActions = []string{"wishCommentWithdraw"}
			return out, nil
		}
		return Comment{}, command.ErrNotFound
	}
	if own {
		body, e := DecryptWishComment(ctx, s.codec, v)
		if e != nil {
			return Comment{}, e
		}
		out.Body = &body
		out.AllowedActions = []string{"wishCommentWithdraw"}
		if w.State == "published" && w.CurrentContentVersion == c.ParentContentVersion && w.CommentsOpen {
			out.AllowedActions = append(out.AllowedActions, "wishCommentUpdate")
		} else if c.Status != "withdrawn" {
			out.Status = "context_changed"
		}
		var task entity.ModerationTask
		if e = db.Where("id=?", v.ReviewTaskID).Take(&task).Error; e != nil {
			return Comment{}, e
		}
		out.ReviewResult = &CommentReview{Decision: task.Status, Reason: task.Reason, RevisionSuggestion: task.RevisionSuggestion}
	} else {
		var n int64
		if e = visibleWishComments(ctx, db, now).Where("c.id=?", id).Count(&n).Error; e != nil {
			return Comment{}, e
		}
		if n != 1 {
			if c.Status == "withdrawn" && c.RootCommentID == nil && c.PublishedAt != nil && w.CurrentContentVersion == c.ParentContentVersion {
				if e = visibleWishComments(ctx, db, now).Where("c.root_comment_id=?", id).Count(&out.ReplyCount).Error; e != nil {
					return Comment{}, e
				}
				if out.ReplyCount > 0 {
					body := "该评论已删除"
					out.Body = &body
					out.Identity = CommentIdentity{Label: "已删除"}
					out.ViewerIsAuthor = false
					return out, nil
				}
			}
			return Comment{}, command.ErrNotFound
		}
		out.Body = v.PublicBody
	}
	author, e := s.ReadAuthorProjection(ctx, db, c.WishID, c.AuthorID)
	if e != nil {
		return Comment{}, e
	}
	out.Identity = CommentIdentity{Label: author.Nickname, IsAuthor: w.OwnerID == c.AuthorID}
	if c.RootCommentID == nil {
		if e = visibleWishComments(ctx, db, now).Where("c.root_comment_id=?", id).Count(&out.ReplyCount).Error; e != nil {
			return Comment{}, e
		}
	}
	if c.ReplyToCommentID != nil {
		var target entity.WishComment
		var visible int64
		if e = visibleWishComments(ctx, db, now).Where("c.id=?", *c.ReplyToCommentID).Count(&visible).Error; e != nil {
			return Comment{}, e
		}
		if visible == 1 {
			if e = db.WithContext(ctx).Where("id=?", *c.ReplyToCommentID).Take(&target).Error; e != nil {
				return Comment{}, e
			}
			identity, e := s.ReadAuthorProjection(ctx, db, c.WishID, target.AuthorID)
			if e != nil {
				return Comment{}, e
			}
			out.ReplyToLabel = identity.Nickname
		}
	}
	return out, nil
}

// GetComment 仅允许本人读取当前稿件和审核结果。
func (s *Service) GetComment(ctx context.Context, token, rid, id string) (out Comment, err error) {
	defer s.queryDone("wish.comment.get", rid, time.Now(), &err)
	if !validWishID(id) {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor, e := currentReader(ctx, db, token, now)
		if e != nil {
			return e
		}
		out, e = s.projectComment(ctx, db, id, actor, true, now)
		return e
	})
	return
}

// ListComments 公开根楼、楼内回复、补充分别分页；本人列表可包含待审或驳回记录。
func (s *Service) ListComments(ctx context.Context, token, rid, wishID string, own bool, q CommentQuery) (out CommentPage, err error) {
	defer s.queryDone("wish.comments.list", rid, time.Now(), &err)
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Kind == "" {
		q.Kind = "comment"
	}
	if q.Limit < 1 || q.Limit > 50 || q.Kind != "comment" && q.Kind != "author_supplement" || !own && !validWishID(wishID) || q.RootCommentID != "" && !validWishID(q.RootCommentID) || q.WishID != "" && !validWishID(q.WishID) {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor := ""
		if token != "" || own {
			var e error
			actor, e = currentReader(ctx, db, token, now)
			if e != nil {
				return e
			}
		}
		scope := "wish.comments/" + s.commentDigest(struct {
			Actor, Wish, Kind, Root string
			Own                     bool
		}{actor, wishID + q.WishID, q.Kind, q.RootCommentID, own})
		after, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		query := db.WithContext(ctx).Model(&entity.WishComment{})
		column, order, compare := "published_at", "ASC", ">"
		if own {
			query = query.Where("author_id=?", actor)
			if q.WishID != "" {
				query = query.Where("wish_id=?", q.WishID)
			}
			column, order, compare = "created_at", "DESC", "<"
		} else {
			a, e := readAggregate(ctx, db, wishID, false)
			if e != nil {
				return e
			}
			if _, e = s.publicContent(ctx, rid, now, a); e != nil {
				return e
			}
			query = query.Where("wish_id=? AND parent_content_version=? AND kind=? AND published_at IS NOT NULL", wishID, a.row.CurrentContentVersion, q.Kind)
			if q.RootCommentID == "" {
				query = query.Where("root_comment_id IS NULL")
			} else {
				query = query.Where("root_comment_id=?", q.RootCommentID)
			}
		}
		if after.ID != "" {
			query = query.Where("("+column+",id)"+compare+"(?,?)", after.PublishedAt, after.ID)
		}
		var rows []entity.WishComment
		if e = query.Order(column + " " + order + ",id " + order).Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Comment{}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			last := rows[len(rows)-1]
			at := last.CreatedAt
			if !own {
				at = *last.PublishedAt
			}
			next := s.encodeCursor(pageCursor{Scope: scope, ID: last.ID, PublishedAt: at})
			out.NextCursor = &next
		}
		for _, row := range rows {
			v, e := s.projectComment(ctx, db, row.ID, actor, own, now)
			if errors.Is(e, command.ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	if err != nil {
		out = CommentPage{}
	}
	return
}

// EnrichInteractionStats 用批量查询补齐列表，计数不改变心愿版本。
func (s *Service) EnrichInteractionStats(ctx context.Context, db *gorm.DB, items []PublicContent, actor string, now time.Time) error {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	stats, e := contentquery.ReadStats(ctx, db, "wish", ids, actor)
	if e != nil {
		return e
	}
	var counts []struct {
		WishID                        string
		CommentCount, SupplementCount int64
	}
	if e = visibleWishComments(ctx, db, now).Where("c.wish_id IN ?", ids).Select("c.wish_id,count(*) FILTER(WHERE c.kind='comment') AS comment_count,count(*) FILTER(WHERE c.kind='author_supplement') AS supplement_count").Group("c.wish_id").Scan(&counts).Error; e != nil {
		return e
	}
	countMap := map[string]struct{ comments, supplements int64 }{}
	for _, r := range counts {
		countMap[r.WishID] = struct{ comments, supplements int64 }{r.CommentCount, r.SupplementCount}
	}
	var rows []entity.Wish
	if e = db.WithContext(ctx).Select("id,comments_open,comments_epoch").Where("id IN ?", ids).Find(&rows).Error; e != nil {
		return e
	}
	settings := map[string]entity.Wish{}
	for _, r := range rows {
		settings[r.ID] = r
	}
	for i := range items {
		st, c, w := stats[items[i].ID], countMap[items[i].ID], settings[items[i].ID]
		items[i].ViewCount = st.ViewCount
		items[i].Favorited = st.Favorited
		items[i].CommentCount = c.comments
		items[i].SupplementCount = c.supplements
		items[i].CommentsOpen = w.CommentsOpen
		items[i].CommentsEpoch = w.CommentsEpoch
	}
	return nil
}

// PublicInteractionContent 对本人也只读取公共投影，收藏和浏览不能从草稿取得摘要。
func (s *Service) PublicInteractionContent(ctx context.Context, token, rid, id string) (out PublicContent, err error) {
	defer s.queryDone("wish.interaction.public", rid, time.Now(), &err)
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		if token != "" {
			if _, e := currentReader(ctx, db, token, now); e != nil {
				return e
			}
		}
		a, e := readAggregate(ctx, db, id, false)
		if e != nil {
			return e
		}
		out, e = s.publicContent(ctx, rid, now, a)
		if e != nil {
			return e
		}
		items := []PublicContent{out}
		if e = s.EnrichInteractionStats(ctx, db, items, "", now); e != nil {
			return e
		}
		out = items[0]
		return nil
	})
	return
}

// ReadPublicationComments 在已授权的后台事务中只返回当前可公开评论及补充。
func (s *Service) ReadPublicationComments(ctx context.Context, db *gorm.DB, now time.Time, id string, offset, limit int) (items []Comment, more bool, err error) {
	var ids []string
	if err = visibleWishComments(ctx, db, now).Where("c.wish_id=?", id).Order("c.published_at,c.id").Offset(offset).Limit(limit+1).Pluck("c.id", &ids).Error; err != nil {
		return
	}
	more = len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	items = []Comment{}
	for _, id := range ids {
		v, e := s.projectComment(ctx, db, id, "", false, now)
		if errors.Is(e, command.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, false, e
		}
		items = append(items, v)
	}
	return
}

// ReadInteractionContent 在互动事务取得参与者及心愿锁后复核公开资格。
func (s *Service) ReadInteractionContent(ctx context.Context, db *gorm.DB, now time.Time, id string) (PublicContent, error) {
	a, e := readAggregate(ctx, db, id, false)
	if e != nil {
		return PublicContent{}, e
	}
	return s.publicContent(ctx, "", now, a)
}
