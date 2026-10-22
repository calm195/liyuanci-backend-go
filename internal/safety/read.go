package safety

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// List 只接受固定列表用途。封禁本人可读警告和申诉，举报类型仍要求普通账号权限。
func (s *Service) List(ctx context.Context, token, rid, op string, q Query) (out Page, err error) {
	defer func() {
		if err != nil {
			out = Page{}
		}
	}()
	defer s.done(op, rid, time.Now(), &err)
	if q.Limit < 1 || q.Limit > 50 {
		return out, command.ErrInvalid
	}
	admin := op == "reports" || op == "admin_appeals"
	if !admin && op != "warnings" && op != "appeals" && op != "report_types" && op != "blocks" {
		return out, command.ErrInvalid
	}
	out.Items = []any{}
	err = s.read(ctx, token, admin, func(db *gorm.DB, actor string, now time.Time) error {
		at, id, e := s.after(actor, op, q.Cursor)
		if e != nil {
			return e
		}
		switch op {
		case "report_types":
			var n int64
			if e = db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", actor, now, now).Count(&n).Error; e != nil {
				return e
			}
			if n > 0 {
				return command.ErrForbidden
			}
			if q.Cursor != "" {
				return command.ErrInvalid
			}
			var rows []entity.ReportType
			if e = db.Where("published").Order("code").Find(&rows).Error; e != nil {
				return e
			}
			for _, r := range rows {
				out.Items = append(out.Items, r)
			}
		case "blocks":
			var n int64
			if e = db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", actor, now, now).Count(&n).Error; e != nil {
				return e
			}
			if n > 0 {
				return command.ErrForbidden
			}
			var rows []entity.Block
			query := db.Where("owner_id=?", actor)
			if id != "" {
				query = query.Where("(created_at,target_id)<(?,?::uuid)", at, id)
			}
			if e = query.Order("created_at DESC,target_id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				c := s.cursor(actor, op, last.CreatedAt, last.TargetID)
				out.NextCursor = &c
			}
			for _, r := range rows {
				out.Items = append(out.Items, map[string]any{"target_user_id": r.TargetID, "version": r.Version})
			}
		case "warnings":
			var rows []entity.Penalty
			query := db.Where("account_id=? AND action_kind='warning' AND revoked_at IS NULL AND acknowledged_at IS NULL AND starts_at<=?", actor, now)
			if id != "" {
				query = query.Where("(starts_at,id)<(?,?::uuid)", at, id)
			}
			if e = query.Order("starts_at DESC,id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				c := s.cursor(actor, op, last.StartsAt, last.ID)
				out.NextCursor = &c
			}
			for _, r := range rows {
				out.Items = append(out.Items, warning(r, now))
			}
		case "reports":
			query := db.Table("public.reports AS r").Select("r.*").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("NOT EXISTS (SELECT 1 FROM public.content_restrictions x WHERE x.content_id=c.id AND x.kind='crisis' AND x.active) AND NOT EXISTS (SELECT 1 FROM public.moderation_tasks t JOIN public.moderation_scans s ON s.task_id=t.id WHERE t.content_id=c.id AND s.crisis_hit)")
			if id != "" {
				query = query.Where("(r.created_at,r.id)<(?,?::uuid)", at, id)
			}
			var rows []entity.Report
			if e = query.Order("r.created_at DESC,r.id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				c := s.cursor(actor, op, last.CreatedAt, last.ID)
				out.NextCursor = &c
			}
			for _, r := range rows {
				out.Items = append(out.Items, Summary{ID: r.ID, Version: r.Version, Status: r.Status, Kind: r.TargetKind})
			}
		case "appeals", "admin_appeals":
			query := db.Table("public.appeals AS a").Select("a.*")
			if !admin {
				query = query.Where("a.account_id=?", actor)
			} else {
				query = query.Joins("JOIN public.penalties p ON p.id=a.penalty_id").Where("p.content_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.content_restrictions x WHERE x.content_id=p.content_id AND x.kind='crisis' AND x.active) AND NOT EXISTS (SELECT 1 FROM public.moderation_tasks t JOIN public.moderation_scans s ON s.task_id=t.id WHERE t.content_id=p.content_id AND s.crisis_hit)")
			}
			if id != "" {
				query = query.Where("(a.created_at,a.id)<(?,?::uuid)", at, id)
			}
			var rows []entity.Appeal
			if e = query.Order("a.created_at DESC,a.id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				c := s.cursor(actor, op, last.CreatedAt, last.ID)
				out.NextCursor = &c
			}
			for _, r := range rows {
				out.Items = append(out.Items, Appeal{r.ID, r.Version, r.Status, r.DecisionReason})
			}
		}
		return nil
	})
	return
}

// Detail 只在具体普通举报/申诉案件授权并记录访问审计后返回加密说明。
// 不提供任意私聊搜索、学生身份或危机案件的跨域读取。
func (s *Service) Detail(ctx context.Context, token, rid, op, id string) (out any, err error) {
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	defer s.done(op, rid, time.Now(), &err)
	if !validID(id) || (op != "report_detail" && op != "appeal_detail") {
		return nil, command.ErrInvalid
	}
	err = s.read(ctx, token, true, func(db *gorm.DB, actor string, _ time.Time) error {
		if op == "report_detail" {
			r, e := report(db, id)
			if e != nil {
				return e
			}
			if r.ContentID == nil {
				return command.ErrNotFound
			}
			c, e := registry(db, *r.ContentID)
			if e != nil {
				return e
			}
			if e = ordinaryCase(db, c.ID); e != nil {
				return e
			}
			description := ""
			if len(r.DescriptionCipher) > 0 {
				description, e = s.decrypt(ctx, "report_description", id, r.DescriptionCipher)
				if e != nil {
					return e
				}
			}
			body, e := s.caseBody(ctx, db, c)
			if e != nil {
				return e
			}
			out = map[string]any{"body": body, "id": r.ID, "version": r.Version, "status": r.Status, "target_kind": r.TargetKind, "target_id": r.TargetID, "type_code": r.TypeCode, "content_ref": c.ID, "content_version": c.ContentVersion, "subject_id": c.AuthorID, "description": description, "reason": r.DecisionReason}
		} else {
			a, e := appeal(db, id)
			if e != nil {
				return e
			}
			p, e := penalty(db, a.PenaltyID)
			if e != nil {
				return e
			}
			if p.ContentID == nil {
				return command.ErrNotFound
			}
			if e = ordinaryCase(db, *p.ContentID); e != nil {
				return e
			}
			body, e := s.decrypt(ctx, "appeal_body", a.ID, a.BodyCipher)
			if e != nil {
				return e
			}
			reason := ""
			if len(p.ReasonCipher) > 0 {
				reason, e = s.decrypt(ctx, "penalty_reason", p.ID, p.ReasonCipher)
				if e != nil {
					return e
				}
			}
			out = map[string]any{"id": a.ID, "version": a.Version, "status": a.Status, "penalty_id": p.ID, "body": body, "penalty_reason": reason, "reason": a.DecisionReason}
		}
		return audit(db, actor, id, op)
	})
	return
}
