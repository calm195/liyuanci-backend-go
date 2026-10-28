package whisper

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func (s *Service) commentState(ctx context.Context, db *gorm.DB, now time.Time, a commentAggregate) (string, error) {
	if a.privacy || a.comment.PostID == nil {
		return "privacy_hold", nil
	}
	if a.isolated {
		return "isolated", nil
	}
	if a.restricted {
		return "restricted", nil
	}
	if a.comment.Lifecycle == "withdrawn" {
		return "withdrawn", nil
	}
	parent, e := loadPost(ctx, db, *a.comment.PostID)
	if e != nil {
		return "context_changed", missingRow(e)
	}
	if parent.privacy || parent.owner.Lifecycle != "active" {
		return "privacy_hold", nil
	}
	if parent.post.CurrentContentVersion != a.comment.ParentContentVersion {
		return "context_changed", nil
	}
	if a.comment.ReviewState != "approved" && (!parent.post.CommentsOpen || parent.post.CommentsEpoch != a.comment.CommentsEpoch) {
		return "context_changed", nil
	}
	if postState(parent) != "published" {
		return "context_changed", nil
	}
	if a.version.ReviewTaskID != nil {
		var task entity.ModerationTask
		if e := reviewTask(ctx, db, *a.version.ReviewTaskID, a.version.ContentRegistryID, a.comment.ContentVersion, &task); e != nil {
			return "", e
		}
		if task.Status == "cancelled" || task.Status == "obsolete" {
			return "cancelled", nil
		}
	}
	if a.comment.ReviewState == "approved" {
		return "published", nil
	}
	if a.comment.ReviewState == "pending" {
		return "review_pending", nil
	}
	return a.comment.ReviewState, nil
}

func (s *Service) ownerComment(ctx context.Context, db *gorm.DB, now time.Time, id, owner string) (out OwnerComment, err error) {
	a, e := loadComment(ctx, db, id)
	if e != nil {
		return out, e
	}
	if a.comment.AuthorID != owner {
		return out, command.ErrNotFound
	}
	state, e := s.commentState(ctx, db, now, a)
	if e != nil {
		return out, e
	}
	c := a.comment
	out = OwnerComment{ID: c.ID, Version: c.Version, ContentVersion: c.ContentVersion, Status: state, CreatedAt: timestamp(c.CreatedAt), UpdatedAt: timestamp(c.UpdatedAt), AllowedActions: []string{"whisperCommentWithdraw"}, PostID: c.PostID, ParentContentVersion: c.ParentContentVersion}
	if c.PublishedAt != nil {
		v := timestamp(*c.PublishedAt)
		out.PublishedAt = &v
	}
	if !slices.Contains([]string{"privacy_hold", "isolated", "restricted"}, state) {
		var body string
		var identity IdentityInput
		if e = s.decrypt(ctx, "whisper_comment_body", id, c.ContentVersion, a.version.BodyCipher, &body); e != nil {
			return OwnerComment{}, e
		}
		if e = s.decrypt(ctx, "whisper_comment_identity", id, c.ContentVersion, a.version.IdentityCipher, &identity); e != nil {
			return OwnerComment{}, e
		}
		out.Body, out.IdentityInput = &body, &identity
	}
	if state != "privacy_hold" && a.version.ReviewTaskID != nil {
		var task entity.ModerationTask
		if e = reviewTask(ctx, db, *a.version.ReviewTaskID, a.version.ContentRegistryID, c.ContentVersion, &task); e != nil {
			return OwnerComment{}, e
		}
		decision := "pending"
		if slices.Contains([]string{"approved", "rejected", "cancelled"}, task.Status) {
			decision = task.Status
		}
		out.ReviewResult = &ReviewResult{Decision: decision, ContentVersion: c.ContentVersion}
		if decision == "rejected" {
			out.ReviewResult.Reason, out.ReviewResult.RevisionSuggestion = task.Reason, task.RevisionSuggestion
		}
	}
	limited, e := restrictedAccount(ctx, db, owner, now)
	if e != nil {
		return OwnerComment{}, e
	}
	if !limited && !slices.Contains([]string{"privacy_hold", "isolated", "restricted"}, state) && c.PostID != nil {
		var parent entity.WhisperPost
		if e = db.WithContext(ctx).Where("id=?", *c.PostID).Take(&parent).Error; e != nil {
			return OwnerComment{}, e
		}
		if _, e = s.commentContext(ctx, db, now, *c.PostID, owner, parent.CommentsEpoch); e == nil {
			out.AllowedActions = append(out.AllowedActions, "whisperCommentUpdate")
		} else if !errors.Is(e, command.ErrNotFound) && !errors.Is(e, command.ErrBlockedInteraction) {
			return OwnerComment{}, e
		}
	}
	// 隐私状态优先抹除旧父帖关联；候选只在完成脱敏并解除父外键后读取。
	if state == "privacy_hold" {
		out.PostID = nil
		if c.PostID == nil && c.Lifecycle == "active" && len(c.RecoveryCipher) > 0 {
			var body string
			if e = s.decrypt(ctx, "whisper_comment_recovery", id, c.ContentVersion, c.RecoveryCipher, &body); e != nil {
				return OwnerComment{}, e
			}
			out.RecoveryBody = &body
		}
	}
	return out, nil
}

func (s *Service) publicComment(ctx context.Context, db *gorm.DB, now time.Time, id string, parent aggregate, viewer audience, c CatalogSnapshot) (out PublicComment, err error) {
	var n int64
	if e := visibleCommentsQuery(ctx, db, parent.post.ID, parent.post.CurrentContentVersion, now).Where("c.id=?", id).Count(&n).Error; e != nil {
		return out, e
	}
	if n != 1 {
		return out, command.ErrNotFound
	}
	a, e := loadComment(ctx, db, id)
	if e != nil {
		return out, e
	}
	var identity PublicIdentity
	if json.Unmarshal(a.version.PublicIdentity, &identity) != nil || identity.Mode != a.version.IdentityMode || !slices.Contains([]string{"anonymous", "semi", "named"}, identity.Mode) {
		return out, command.ErrNotFound
	}
	var alias entity.WhisperAlias
	if e = db.WithContext(ctx).Where("post_id=? AND actor_id=?", parent.post.ID, a.comment.AuthorID).Take(&alias).Error; e != nil {
		return out, e
	}
	identity.IsAuthor = a.comment.AuthorID == parent.post.OwnerID
	if identity.Mode == "semi" && (a.author.HideIdentityTags || !tagsValid(identity.Tags, c.Grades)) {
		identity.Mode, identity.Tags = "anonymous", nil
	}
	if identity.Mode != "named" {
		identity.Label = alias.Label
	} else if !validText(identity.Label, 2, 20) {
		return out, command.ErrNotFound
	}
	if identity.Mode != "semi" {
		identity.Tags = nil
	}
	out = PublicComment{ID: id, Version: a.comment.Version, ContentVersion: a.comment.ContentVersion, Body: *a.version.PublicBody, Identity: identity, PublishedAt: timestamp(*a.comment.PublishedAt), SchoolRelated: *a.version.SchoolRelated, NoticeCodes: []string{}, ViewerIsAuthor: viewer.ID == a.comment.AuthorID, AllowedActions: []string{}, PostID: parent.post.ID, ParentContentVersion: a.comment.ParentContentVersion}
	if out.SchoolRelated {
		out.NoticeCodes = []string{"UNVERIFIED_SCHOOL"}
		v := "未经证实"
		out.NoticeText = &v
	}
	if viewer.ID != "" && !viewer.Restricted && !out.ViewerIsAuthor {
		out.AllowedActions = append(out.AllowedActions, "whisperReportCreate")
	}
	return out, nil
}

// GetComment 仅提供本人当前投影，不提供独立公开原稿详情入口。
func (s *Service) GetComment(ctx context.Context, token, rid, id string) (out OwnerComment, err error) {
	defer s.completed("comment_get", rid, time.Now(), &err)
	if !s.config.Enabled {
		return out, ErrModuleUnavailable
	}
	if !ValidID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		a, e := reader(ctx, db, token, now, false)
		if e != nil {
			return e
		}
		out, e = s.ownerComment(ctx, db, now, id, a.ID)
		return e
	})
	if err != nil {
		out = OwnerComment{}
	}
	return
}

// ListComments 公开列表按发布时间正序，本人列表按创建时间倒序；候选游标独立于可见项。
func (s *Service) ListComments(ctx context.Context, token, rid, postID string, own bool, q Query) (out any, err error) {
	defer s.completed("comments_list", rid, time.Now(), &err)
	if !s.config.Enabled {
		return nil, ErrModuleUnavailable
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 || !own && !ValidID(postID) {
		return nil, command.ErrInvalid
	}
	c, e := s.resources(ctx)
	if e != nil {
		return nil, e
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		viewer, e := reader(ctx, db, token, now, !own)
		if e != nil {
			return e
		}
		scope := s.scope("comments/public/"+postID, viewer.ID)
		if own {
			scope = s.scope("comments/own", viewer.ID)
		}
		after, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		var parent aggregate
		query := db.WithContext(ctx).Model(&entity.WhisperComment{})
		column, order, compare := "published_at", "ASC", ">"
		if own {
			query = query.Where("author_id=?", viewer.ID)
			column, order, compare = "created_at", "DESC", "<"
		} else {
			if _, e = s.publicPostBase(ctx, db, now, postID, viewer, c); e != nil {
				return e
			}
			parent, e = loadPost(ctx, db, postID)
			if e != nil {
				return e
			}
			query = query.Where("post_id=? AND published_at IS NOT NULL", postID)
		}
		if after.ID != "" {
			query = query.Where("("+column+",id)"+compare+"(?,?)", after.At, after.ID)
		}
		var rows []entity.WhisperComment
		if e = query.Order(column + " " + order + ",id " + order).Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		var next *string
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			last := rows[len(rows)-1]
			at := last.CreatedAt
			if !own {
				at = *last.PublishedAt
			}
			v := s.encodeCursor(cursor{scope, last.ID, at})
			next = &v
		}
		if own {
			page := Page[OwnerComment]{Items: []OwnerComment{}, NextCursor: next}
			for _, r := range rows {
				v, e := s.ownerComment(ctx, db, now, r.ID, viewer.ID)
				if e != nil {
					return e
				}
				page.Items = append(page.Items, v)
			}
			out = page
		} else {
			page := Page[PublicComment]{Items: []PublicComment{}, NextCursor: next}
			for _, r := range rows {
				v, e := s.publicComment(ctx, db, now, r.ID, parent, viewer, c)
				if errors.Is(e, command.ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				page.Items = append(page.Items, v)
			}
			out = page
		}
		return nil
	})
	if err != nil {
		out = nil
	}
	return
}
