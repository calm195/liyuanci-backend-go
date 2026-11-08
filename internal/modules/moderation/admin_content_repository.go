package moderation

import (
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func (s *AdminContent) summaries(db *gorm.DB, now time.Time) (q *gorm.DB, table, state string) {
	if s.wishes != nil {
		table = "public.wishes"
		state = "CASE WHEN c.state='published' AND c.expires_at<=? THEN 'expired' ELSE c.state END"
		q = db.Table(table+" AS c").Select(
			"c.id,c.created_at,c.published_at,("+state+") AS state,"+
				"requester.nickname AS requester,COALESCE(helper.nickname,'') AS helper,m.started_at AS matched_at", now).
			Joins("JOIN public.accounts requester ON requester.id=c.owner_id AND requester.lifecycle='active'").
			Joins("LEFT JOIN public.matches m ON m.wish_id=c.id AND m.status='current'").
			Joins("LEFT JOIN public.accounts helper ON helper.id=m.helper_id AND helper.lifecycle='active'").
			Where("c.draft_deleted_at IS NULL")
	} else {
		table = "public.whisper_posts"
		state = `CASE
			WHEN EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry r ON r.id=x.content_id
				WHERE r.kind='whisper_post' AND r.entity_id=c.id AND x.active AND x.kind='crisis') THEN 'isolated'
			WHEN EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry r ON r.id=x.content_id
				WHERE r.kind='whisper_post' AND r.entity_id=c.id AND x.active) THEN 'restricted'
			WHEN c.lifecycle='withdrawn' THEN 'withdrawn'
			WHEN c.review_state='approved' AND c.publish_intent AND c.published_content_version=c.current_content_version THEN 'published'
			WHEN c.review_state='approved' THEN 'cancelled'
			WHEN c.review_state='pending' THEN 'review_pending'
			ELSE c.review_state END`
		q = db.Table(table + " AS c").Select("c.id,c.created_at,c.published_at,(" + state + ") AS state").
			Joins("JOIN public.accounts owner ON owner.id=c.owner_id AND owner.lifecycle='active'")
	}
	targetKind := "wish"
	if s.whisper != nil {
		targetKind = "whisper_post"
	}
	q = q.Where(`NOT EXISTS(SELECT 1 FROM public.privacy_holds h
		WHERE h.target_kind=? AND h.target_id=c.id AND h.status='active')`, targetKind)
	return
}

func (s *AdminContent) summary(db *gorm.DB, id string, now time.Time) (out AdminContentSummary, err error) {
	q, _, _ := s.summaries(db, now)
	res := q.Where("c.id=?", id).Scan(&out)
	if res.Error == nil && res.RowsAffected == 0 {
		return out, command.ErrNotFound
	}
	return out, res.Error
}

func (s *AdminContent) candidates(db *gorm.DB, filters AdminContentQuery, now time.Time, after string, limit int) (out []AdminContentSummary, err error) {
	q, table, state := s.summaries(db, now)
	if filters.State != "" {
		if s.wishes != nil {
			q = q.Where("("+state+")=?", now, filters.State)
		} else {
			q = q.Where("("+state+")=?", filters.State)
		}
	}
	if filters.Requester != "" {
		q = q.Where("strpos(lower(requester.nickname),lower(?))>0", filters.Requester)
	}
	if filters.Helper != "" {
		q = q.Where("strpos(lower(helper.nickname),lower(?))>0", filters.Helper)
	}
	if !filters.From.IsZero() {
		q = q.Where("c.created_at>=?", filters.From)
	}
	if !filters.Until.IsZero() {
		q = q.Where("c.created_at<?", filters.Until)
	}
	if after != "" {
		q = q.Where("(c.created_at,c.id)<(SELECT created_at,id FROM "+table+" WHERE id=?)", after)
	}
	if filters.Keyword != "" {
		// 先由数据库找字面匹配，再复核公开资格，避免遍历大量无关内容。
		value := strings.ToLower(filters.Keyword)
		if s.wishes != nil {
			q = q.Joins("LEFT JOIN public.wish_versions v ON v.wish_id=c.id AND v.content_version=c.current_content_version").
				Joins("LEFT JOIN public.story_versions story ON story.wish_id=c.id AND story.story_version=c.current_story_version").
				Where("strpos(lower(COALESCE(v.public_title,'未命名心愿')),?)>0 OR strpos(lower(v.public_body),?)>0 OR strpos(lower(story.public_body),?)>0", value, value, value)
		} else {
			q = q.Joins("JOIN public.whisper_post_versions v ON v.post_id=c.id AND v.content_version=c.current_content_version").
				Where("strpos(lower(COALESCE(v.public_title,'未命名悄悄话')),?)>0 OR strpos(lower(v.public_body),?)>0", value, value)
		}
	}
	err = q.Order("c.created_at DESC,c.id DESC").Limit(limit).Scan(&out).Error
	return
}
