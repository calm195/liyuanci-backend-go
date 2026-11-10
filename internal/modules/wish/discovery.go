package wish

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// SetCandidateCache 在服务启动前注入候选缓存，不改变当前公开资格复核。
func (s *Service) SetCandidateCache(cache contentquery.CandidateCache) {
	s.candidateCache = cache
}

// List 组合当前公开心愿的分类、校区、关键词与排序。
func (s *Service) List(ctx context.Context, rid string, q Query) (PublicPage, error) {
	return s.discoverWishes(ctx, rid, q, false)
}

// Weekly 读取最近七天有有效应愿、浏览或支持的公开心愿。
func (s *Service) Weekly(ctx context.Context, rid string) (PublicPage, error) {
	return s.discoverWishes(ctx, rid, Query{Sort: contentquery.SortHot, Limit: contentquery.WeeklyLimit}, true)
}

func (s *Service) discoverWishes(ctx context.Context, rid string, q Query, weekly bool) (out PublicPage, err error) {
	defer s.storyReadDone("wish.discovery", rid, time.Now(), &err)
	if e := validateQuery(q, false); e != nil {
		return out, e
	}
	sort := q.Sort
	if sort == SortSupport {
		sort = contentquery.SortTime
	}
	q.Keyword, err = contentquery.Normalize(sort, q.Keyword)
	if err != nil {
		return out, err
	}
	scope := queryScope("public", "", q)
	key := ""
	if weekly {
		// 支持提交后立即反映在榜单中，避免并发读将旧候选重新写入缓存。
		scope = contentquery.WishWeeklyKey
	} else if q.Keyword == "" && q.Category == "" && q.Campus == "" && q.Sort != contentquery.SortHot && q.Sort != SortSupport {
		key = contentquery.WishListKey
	}
	hot := q.Sort == contentquery.SortHot
	return s.discover(ctx, scope, key, q, weekly, func(db *gorm.DB, now time.Time) *gorm.DB {
		query := db.Table("public.wishes AS w").
			Where("w.draft_deleted_at IS NULL AND w.state='published' AND w.published_at IS NOT NULL AND w.expires_at>?", now)
		if q.Category != "" {
			query = query.Where("w.category=?", q.Category)
		}
		if q.Campus != "" {
			query = query.Where("w.campus=?", q.Campus)
		}
		if q.Keyword != "" {
			query = query.Joins("JOIN public.wish_versions v ON v.wish_id=w.id AND v.content_version=w.current_content_version")
			query = contentquery.Match(query, "COALESCE(NULLIF(v.public_title,''),'未命名心愿')", "v.public_body", q.Keyword)
		}
		supports := activeSupports(db).Select("count(*)").Where("ws.wish_id=w.id")
		if q.Sort == SortSupport {
			return query.Select("w.id,w.published_at AS at,(?) AS score", supports)
		}
		if !hot {
			return query.Select("w.id,w.published_at AS at")
		}
		score := db.Table("public.applications AS a").Select("count(DISTINCT a.applicant_id)").
			Joins("JOIN public.accounts actor ON actor.id=a.applicant_id AND actor.lifecycle='active' AND actor.verification='verified'").
			Joins("JOIN public.accounts owner ON owner.id=w.owner_id AND owner.auth_epoch=a.owner_epoch").
			Joins("JOIN public.moderation_tasks t ON t.id=a.review_task_id AND t.status='approved' AND t.decision='approved' AND t.subject_epoch=actor.auth_epoch AND t.content_version=a.content_version AND t.restriction_epoch=w.restriction_epoch").
			Joins("JOIN public.content_registry r ON r.id=t.content_id AND r.kind='application' AND r.entity_id=a.id AND r.content_version=a.content_version AND r.status='approved'").
			Where("a.wish_id=w.id AND a.wish_content_version=w.current_content_version AND a.status='active' AND a.applicant_id<>w.owner_id").
			Where("t.hold_epoch=COALESCE((SELECT max(h.hold_epoch) FROM public.privacy_holds h WHERE h.target_kind='wish' AND h.target_id=w.id),0)").
			Where("NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=r.id AND x.active)").
			Where("NOT EXISTS(SELECT 1 FROM public.blocks b WHERE (b.owner_id=w.owner_id AND b.target_id=a.applicant_id) OR (b.owner_id=a.applicant_id AND b.target_id=w.owner_id))").
			Where("NOT EXISTS(SELECT 1 FROM public.penalties p WHERE p.account_id=actor.id AND p.revoked_at IS NULL AND p.action_kind IN ('mute','ban') AND p.starts_at<=? AND (p.ends_at IS NULL OR p.ends_at>?))", now, now)
		views := db.Table("public.wish_detail_views AS v").Select("count(*)").Where("v.wish_id=w.id")
		if weekly {
			cutoff := now.Add(-7 * 24 * time.Hour)
			score = score.Where("a.created_at>=? AND a.created_at<=?", cutoff, now)
			views = views.Where("v.viewed_at>=? AND v.viewed_at<=?", cutoff, now)
			supports = supports.Where("ws.created_at>=? AND ws.created_at<=?", cutoff, now)
			return query.Select("w.id,w.published_at AS at,((?)+(?)+(?)) AS score", score, views, supports)
		}
		return query.Select("w.id,w.published_at AS at,((?)+(?)) AS score", score, views)
	}, func(db *gorm.DB, now time.Time, id, actor string) (PublicContent, error) {
		a, e := readAggregate(ctx, db, id, false)
		if e != nil {
			return PublicContent{}, e
		}
		out, e := s.publicContent(rid, now, a)
		if e == nil {
			state, readErr := readSupport(db, id, actor)
			out.Support, e = &state, readErr
		}
		return out, e
	})
}

// Stories 保留默认的公开故事时间排序入口。
func (s *Service) Stories(ctx context.Context, rid, cursor string, limit int) (PublicPage, error) {
	return s.SearchStories(ctx, rid, Query{Cursor: cursor, Limit: limit})
}

// SearchStories 使用还愿公开正文和关联心愿公开标题查询当前版本。
func (s *Service) SearchStories(ctx context.Context, rid string, q Query) (out PublicPage, err error) {
	defer s.storyReadDone("stories.list", rid, time.Now(), &err)
	if q.Limit < 1 || q.Limit > 50 {
		return out, command.ErrInvalid
	}
	q.Keyword, err = contentquery.Normalize(q.Sort, q.Keyword)
	if err != nil {
		return out, err
	}
	scope, key := "stories/public", contentquery.StoryListKey
	hot := q.Sort == contentquery.SortHot
	if hot || q.Keyword != "" {
		scope, key = queryScope(scope, "", q), ""
	}
	return s.discover(ctx, scope, key, q, false, func(db *gorm.DB, _ time.Time) *gorm.DB {
		query := db.Table("public.wishes AS w").
			Joins("JOIN public.story_versions v ON v.wish_id=w.id AND v.story_version=w.current_story_version").
			Where("w.state='fulfilled' AND w.current_story_version>0 AND w.draft_deleted_at IS NULL")
		if q.Keyword != "" {
			query = query.Joins("JOIN public.wish_versions title ON title.wish_id=w.id AND title.content_version=w.current_content_version")
			query = contentquery.Match(query, "COALESCE(NULLIF(title.public_title,''),'未命名心愿')", "v.public_body", q.Keyword)
		}
		if !hot {
			return query.Select("w.id,v.created_at AS at")
		}
		return query.Select(`w.id,v.created_at AS at,
			(SELECT count(*) FROM public.story_views x JOIN public.accounts viewer ON viewer.id=x.viewer_id
			 WHERE x.wish_id=w.id AND viewer.lifecycle='active') AS score`)
	}, func(db *gorm.DB, _ time.Time, id, _ string) (PublicContent, error) {
		return s.publicStory(ctx, db, id)
	})
}

func (s *Service) discover(ctx context.Context, scope, key string, q Query, weekly bool,
	base func(*gorm.DB, time.Time) *gorm.DB,
	project func(*gorm.DB, time.Time, string, string) (PublicContent, error),
) (out PublicPage, err error) {
	after, err := s.decodeCursor(q.Cursor, scope)
	if err != nil || after.ID != "" && after.PublishedAt.IsZero() {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor := ""
		if q.ViewerToken != "" {
			var e error
			actor, e = currentReader(ctx, db, q.ViewerToken, now)
			if e != nil {
				return e
			}
		}
		fetch := func(position contentquery.Candidate, limit int) (rows []contentquery.Candidate, e error) {
			query := contentquery.Ordered(db, base(db, now), position, q.Sort == contentquery.SortHot || q.Sort == SortSupport)
			if weekly {
				query = query.Where("score>0")
			}
			e = query.Limit(limit).Scan(&rows).Error
			return
		}
		position := contentquery.Candidate{ID: after.ID, At: after.PublishedAt, Score: after.Score}
		items, next, e := contentquery.Collect(ctx, s.candidateCache, key, position, q.Limit, fetch, func(candidate contentquery.Candidate) (PublicContent, error) {
			item, err := project(db, now, candidate.ID, actor)
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
			encoded := s.encodeCursor(pageCursor{Scope: scope, ID: next.ID, PublishedAt: next.At, Score: next.Score})
			out.NextCursor = &encoded
		}
		return nil
	})
	if err != nil {
		out = PublicPage{}
	}
	return
}

func (s *Service) recordStoryView(ctx context.Context, rid, id, viewer string, version int64) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	// 去重统计是可恢复的旁路操作，失败仍保留已获授权的详情响应。
	err := s.db.WithContext(ctx).Exec(`INSERT INTO public.story_views(wish_id,viewer_id,viewed_at)
		SELECT w.id,a.id,clock_timestamp() FROM public.wishes w CROSS JOIN public.accounts a
		WHERE w.id=? AND w.current_story_version=? AND w.state='fulfilled' AND a.id=? AND a.lifecycle='active'
		ON CONFLICT(wish_id,viewer_id) DO NOTHING`, id, version, viewer).Error
	s.queryDone("story.view", rid, start, &err)
}

func (s *Service) recordWishDetailView(ctx context.Context, rid, id string, version int64) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err := s.db.WithContext(ctx).Exec(`INSERT INTO public.wish_detail_views(wish_id,viewed_at)
		SELECT id,clock_timestamp() FROM public.wishes
		WHERE id=? AND current_content_version=? AND state='published'
		  AND draft_deleted_at IS NULL AND expires_at>clock_timestamp()`, id, version).Error
	s.queryDone("wish.view", rid, start, &err)
}
