package resourceplaza

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Read 执行八个固定查询操作；每页重新验证数据库主体和游标绑定。
func (s *Service) Read(ctx context.Context, token, rid, op, id string, q Query, admin bool) (out any, err error) {
	defer s.done(op, rid, time.Now(), &err)
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	err = s.transaction(ctx, token, admin, false, func(db *gorm.DB, a actor) error {
		if op == "categories" {
			after, e := s.after(a, op, q)
			if e != nil {
				return e
			}
			v := db.Model(&entity.ResourceCategory{})
			if !admin {
				v = v.Where("enabled")
			} else if q.Enabled != "" {
				v = v.Where("enabled=?", q.Enabled == "true")
			}
			if after.ID != "" {
				v = v.Where("(sort_order,id)>(?,?::uuid)", after.Sort, after.ID)
			}
			var rows []entity.ResourceCategory
			if e = v.Order("sort_order ASC,id ASC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			var next *string
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				next, e = s.next(a, op, q, cursor{ID: last.ID, Sort: last.SortOrder})
				if e != nil {
					return e
				}
			}
			items := []any{}
			for _, c := range rows {
				items = append(items, catProjection(c, admin))
			}
			out = map[string]any{"items": items, "next_cursor": next}
			return nil
		}
		if op == "category_detail" {
			c, e := category(db, id)
			if e != nil {
				return e
			}
			out = catProjection(c, true)
			return nil
		}
		if op == "list" {
			after, e := s.after(a, op, q)
			if e != nil {
				return e
			}
			if q.CategoryID != "" {
				c, e := category(db, q.CategoryID)
				if errors.Is(e, command.ErrNotFound) || e == nil && !c.Enabled {
					return command.ErrInvalid
				}
				if e != nil {
					return e
				}
			}
			v := db.Model(&entity.ResourceMaterial{}).Omit("download_cipher").Where("NOT privacy_blocked")
			if !admin {
				v = v.Where("status='published' AND NOT content_blocked")
			}
			if q.CategoryID != "" {
				v = v.Where("category_id=?", q.CategoryID)
			}
			if q.Status != "" {
				v = v.Where("status=?", q.Status)
			}
			if q.Q != "" {
				v = v.Where("strpos(translate(title,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'),?)>0 OR strpos(translate(description,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'),?)>0", Fold(q.Q), Fold(q.Q))
			}
			if after.ID != "" {
				v = v.Where("(created_at,id)<(?,?::uuid)", after.At, after.ID)
			}
			var rows []entity.ResourceMaterial
			if e = v.Order("created_at DESC,id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			var next *string
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				next, e = s.next(a, op, q, cursor{ID: last.ID, At: last.CreatedAt})
				if e != nil {
					return e
				}
			}
			stats := map[string]map[string]any{}
			if admin {
				stats, e = summaries(db, rows)
				if e != nil {
					return e
				}
			}
			categoryIDs := []string{}
			for _, r := range rows {
				categoryIDs = append(categoryIDs, r.CategoryID)
			}
			categories := map[string]entity.ResourceCategory{}
			var categoryRows []entity.ResourceCategory
			if len(categoryIDs) > 0 {
				if e = db.Where("id IN ?", categoryIDs).Find(&categoryRows).Error; e != nil {
					return e
				}
			}
			for _, c := range categoryRows {
				categories[c.ID] = c
			}
			items := []any{}
			for _, r := range rows {
				p, e := s.project(db, a, r, false, stats, categories)
				if e != nil {
					return e
				}
				items = append(items, p)
			}
			out = map[string]any{"items": items, "next_cursor": next}
			return nil
		}
		r, e := material(db, id, a)
		if e != nil {
			return e
		}
		if op == "download" {
			c, e := category(db, r.CategoryID)
			if e != nil || !c.Enabled {
				return command.ErrUnavailable
			}
			if r.Version != q.ExpectedVersion {
				return command.ErrVersionConflict
			}
			d, e := s.download(ctx, r)
			if e != nil {
				return e
			}
			copyText := d.URL
			if d.Code != nil {
				copyText += "\n提取码：" + *d.Code
			}
			out = map[string]any{"resource_id": r.ID, "version": r.Version, "download_url": d.URL, "extraction_code": d.Code, "copy_text": copyText}
			return nil
		}
		stats := map[string]map[string]any{}
		if admin {
			stats, e = summaries(db, []entity.ResourceMaterial{r})
			if e != nil {
				return e
			}
		}
		out, e = s.project(db, a, r, true, stats, nil)
		return e
	})
	return
}
