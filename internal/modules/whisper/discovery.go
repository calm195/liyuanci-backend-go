package whisper

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
)

// SetCandidateCache 在服务启动前注入缓存，公开字段和会话信息仍由数据库实时投影。
func (s *Service) SetCandidateCache(cache contentquery.CandidateCache) {
	s.candidateCache = cache
}

// Weekly 返回最近七天收到有效抱抱或可见暖评的帖子。
func (s *Service) Weekly(ctx context.Context, token, rid string) (out Page[PublicPost], err error) {
	defer s.completed("posts_weekly", rid, time.Now(), &err)
	if !s.config.Enabled {
		return out, ErrModuleUnavailable
	}
	return s.discoverPosts(ctx, token, Query{Sort: contentquery.SortHot, Limit: contentquery.WeeklyLimit}, true)
}

func (s *Service) discoverPosts(ctx context.Context, token string, q Query, weekly bool) (out Page[PublicPost], err error) {
	q.Keyword, err = contentquery.Normalize(q.Sort, q.Keyword)
	if err != nil {
		return out, err
	}
	catalog, err := s.resources(ctx)
	if err != nil {
		return out, err
	}
	hot := q.Sort == contentquery.SortHot
	kind, key := "public", ""
	switch {
	case weekly:
		kind, key = contentquery.WhisperWeeklyKey, contentquery.WhisperWeeklyKey
	case hot || q.Keyword != "":
		fields, _ := json.Marshal([]any{hot, q.Keyword})
		kind += string(fields)
	default:
		key = contentquery.WhisperListKey
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		viewer, e := reader(ctx, db, token, now, true)
		if e != nil {
			return e
		}
		scope := s.scope(kind, viewer.ID)
		after, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		fetch := func(position contentquery.Candidate, limit int) (rows []contentquery.Candidate, e error) {
			base := publicPostCandidates(db, q.Keyword, hot, weekly, now)
			query := contentquery.Ordered(db, base, position, hot)
			if weekly {
				query = query.Where("score>0")
			}
			e = query.Limit(limit).Scan(&rows).Error
			return
		}
		position := contentquery.Candidate{ID: after.ID, At: after.At, Score: after.Score}
		items, next, e := contentquery.Collect(ctx, s.candidateCache, key, position, q.Limit, fetch, func(candidate contentquery.Candidate) (PublicPost, error) {
			item, err := s.publicPost(ctx, db, now, candidate.ID, viewer, catalog)
			if weekly {
				item.HeatScore = candidate.Score
			}
			return item, err
		})
		if e != nil {
			return e
		}
		out.Items = items
		if next.ID != "" && !weekly {
			encoded := s.encodeCursor(cursor{Scope: scope, ID: next.ID, At: next.At, Score: next.Score})
			out.NextCursor = &encoded
		}
		return nil
	})
	if err != nil {
		out = Page[PublicPost]{}
	}
	return
}

func publicPostCandidates(db *gorm.DB, keyword string, hot, weekly bool, now time.Time) *gorm.DB {
	q := db.Table("public.whisper_posts AS post").Where("post.published_content_version IS NOT NULL")
	if keyword != "" {
		q = q.Joins("JOIN public.whisper_post_versions v ON v.post_id=post.id AND v.content_version=post.published_content_version")
		q = contentquery.Match(q, "COALESCE(v.public_title,'未命名悄悄话')", "v.public_body", keyword)
	}
	if !hot {
		return q.Select("post.id,post.published_at AS at")
	}
	hugs := activeHugs(db, now).Select("count(*)").Where("h.post_id=post.id")
	comments := visibleComments(db, now).Select("count(*)").
		Where("c.post_id=post.id AND c.parent_content_version=post.published_content_version")
	if weekly {
		cutoff := now.Add(-7 * 24 * time.Hour)
		hugs = hugs.Where("h.activated_at>=? AND h.activated_at<=?", cutoff, now)
		comments = comments.Where("c.published_at>=? AND c.published_at<=?", cutoff, now)
	}
	return q.Select("post.id,post.published_at AS at,((?)+(?) ) AS score", hugs, comments)
}
