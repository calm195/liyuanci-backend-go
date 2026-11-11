package moderation

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// CaseQuery 仅筛选已有授权范围内的普通审核元数据，不批量解密正文。
type CaseQuery struct {
	Q      string
	Status string
	Kind   string
	Cursor string
	Limit  int
}

func normalizeCaseQuery(q CaseQuery) (CaseQuery, error) {
	q.Q = strings.TrimSpace(q.Q)
	if q.Status == "" {
		q.Status = "human_required"
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if !utf8.ValidString(q.Q) || utf8.RuneCountInString(q.Q) > 80 || strings.ContainsRune(q.Q, 0) ||
		!slices.Contains([]string{"human_required", "all", "approved", "rejected"}, q.Status) ||
		!slices.Contains([]string{"", "wish", "application", "message", "story"}, q.Kind) || q.Limit < 1 || q.Limit > 100 {
		return q, command.ErrInvalid
	}
	return q, nil
}

func caseRank(status string) int {
	if status == "human_required" {
		return 0
	}
	return 1
}

func (s *Service) casePurpose(q CaseQuery) string {
	return "cases.v2:" + s.digest(struct{ Q, Status, Kind string }{q.Q, q.Status, q.Kind})
}

func (s *Service) caseAfter(actor string, q CaseQuery) (id string, rank int, err error) {
	if q.Cursor == "" {
		return "", 0, nil
	}
	// 排序组写入签名用途而非重新查询锚点状态，避免案件审核后游标跳组。
	for group, suffix := range []string{":0", ":1"} {
		if id, err = s.after(actor, s.casePurpose(q)+suffix, q.Cursor); err == nil {
			return id, group, nil
		}
	}
	return "", 0, command.ErrInvalid
}

// QueryCases 在SQL分页前应用授权、编号搜索、状态和类型筛选，再按需审核优先排序。
func (s *Service) QueryCases(ctx context.Context, token, rid string, input CaseQuery) (out Page, err error) {
	defer func() {
		if err != nil {
			out = Page{}
		}
	}()
	defer s.done("cases.list", rid, time.Now(), &err)
	input, err = normalizeCaseQuery(input)
	if err != nil {
		return
	}
	out.Items = []Summary{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorize(db.Statement.Context, db, token, command.Discovery{})
		if e != nil {
			return e
		}
		after, rank, e := s.caseAfter(actor, input)
		if e != nil {
			return e
		}
		q := db.Model(&entity.ModerationTask{}).Table("public.moderation_tasks AS t").
			Select("t.id,t.version,t.status,c.kind,t.content_version").
			Joins("JOIN public.moderation_scans s ON s.task_id=t.id").
			Joins("JOIN public.content_registry c ON c.id=t.content_id AND c.kind IN ('wish','application','message','story')").
			Joins("JOIN public.accounts a ON a.id=c.author_id AND a.lifecycle='active'").
			Where("NOT EXISTS(SELECT 1 FROM public.content_restrictions r WHERE r.content_id=c.id AND r.kind='crisis' AND r.active) AND NOT s.crisis_hit AND (s.ordinary_hit OR (c.kind<>'message' AND s.school_related)) AND t.status IN ?", []string{"human_required", "approved", "rejected"})
		if input.Status != "all" {
			q = q.Where("t.status=?", input.Status)
		}
		if input.Kind != "" {
			q = q.Where("c.kind=?", input.Kind)
		}
		if input.Q != "" {
			// 只搜索现有可见元数据编号；未知正文关键词自然无结果，不触发敏感内容解密。
			q = q.Where("strpos(lower(t.id::text),lower(?))>0 OR strpos(lower(c.id::text),lower(?))>0 OR strpos(lower(c.entity_id::text),lower(?))>0", input.Q, input.Q, input.Q)
		}
		const priority = "CASE WHEN t.status='human_required' THEN 0 ELSE 1 END"
		if after != "" {
			q = q.Where("("+priority+">?) OR ("+priority+"=? AND t.id>?)", rank, rank, after)
		}
		var rows []Summary
		if e = q.Order(priority + ",t.id").Limit(input.Limit + 1).Scan(&rows).Error; e != nil {
			return e
		}
		if len(rows) > input.Limit {
			rows = rows[:input.Limit]
			last := rows[len(rows)-1]
			suffix := ":0"
			if caseRank(last.Status) == 1 {
				suffix = ":1"
			}
			next := s.cursor(actor, s.casePurpose(input)+suffix, last.ID)
			out.NextCursor = &next
		}
		out.Items = rows
		if out.Items == nil {
			out.Items = []Summary{}
		}
		return nil
	})
	return
}
