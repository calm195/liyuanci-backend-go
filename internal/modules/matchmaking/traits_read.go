package matchmaking

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func normalizeTraitQuery(q TraitQuery, max int) (TraitQuery, error) {
	q.Q = strings.TrimSpace(q.Q)
	if q.Purpose == "" {
		q.Purpose = "all"
	}
	if q.Status == "" {
		q.Status = "approved"
	}
	if q.Sort == "" {
		q.Sort = "heat_desc"
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if !slices.Contains([]string{"all", "personal", "ideal"}, q.Purpose) || !utf8.ValidString(q.Q) || utf8.RuneCountInString(q.Q) > 100 || !slices.Contains([]string{"", "male", "female", "undisclosed"}, q.Gender) || !slices.Contains([]string{"approved", "pending", "rejected"}, q.Status) || !slices.Contains([]string{"heat_desc", "heat_asc"}, q.Sort) || q.Page < 1 || q.Page > 10000 || q.Limit < 1 || q.Limit > max || q.MinHeat != nil && *q.MinHeat < 0 || q.MaxHeat != nil && *q.MaxHeat < 0 || q.MinHeat != nil && q.MaxHeat != nil && *q.MinHeat > *q.MaxHeat {
		return q, command.ErrInvalid
	}
	return q, nil
}

func traitAggregate(db *gorm.DB, q TraitQuery) *gorm.DB {
	// 统计实时读取账号当前性别，注销中的账号立即退出；不存易漂移的计数缓存。
	counts := db.Table("public.matchmaking_trait_selections s").Select(`s.trait_id,
 COUNT(DISTINCT s.account_id) AS all_count,
 COUNT(DISTINCT s.account_id) FILTER (WHERE a.identity_tags->>'gender'='male') AS male_count,
 COUNT(DISTINCT s.account_id) FILTER (WHERE a.identity_tags->>'gender'='female') AS female_count,
 COUNT(DISTINCT s.account_id) FILTER (WHERE COALESCE(a.identity_tags->>'gender','') NOT IN ('male','female')) AS undisclosed_count`).
		Joins("JOIN public.accounts a ON a.id=s.account_id AND a.lifecycle='active'").Group("s.trait_id")
	if q.Purpose != "" && q.Purpose != "all" {
		counts = counts.Where("s.purpose=?", q.Purpose)
	}
	heat := "COALESCE(c.all_count,0)"
	switch q.Gender {
	case "male":
		heat = "COALESCE(c.male_count,0)"
	case "female":
		heat = "COALESCE(c.female_count,0)"
	case "undisclosed":
		heat = "COALESCE(c.undisclosed_count,0)"
	}
	base := db.Table("public.matchmaking_traits t").Select("t.id,t.name,t.status,t.version,t.created_at,t.reason,"+heat+" AS heat,COALESCE(c.male_count,0) AS male_count,COALESCE(c.female_count,0) AS female_count,COALESCE(c.undisclosed_count,0) AS undisclosed_count").Joins("LEFT JOIN (?) c ON c.trait_id=t.id", counts).Where("t.status=?", q.Status)
	if q.Q != "" {
		base = base.Where("strpos(lower(t.name),lower(?))>0", q.Q)
	}
	if q.Gender != "" {
		base = base.Where(heat + ">0")
	}
	if q.MinHeat != nil {
		base = base.Where(heat+">=?", *q.MinHeat)
	}
	if q.MaxHeat != nil {
		base = base.Where(heat+"<=?", *q.MaxHeat)
	}
	return base
}

func traitPage(db *gorm.DB, q TraitQuery, rank bool) (out TraitPage, err error) {
	out.Items = []Trait{}
	base := traitAggregate(db, q)
	if err = db.Table("(?) matched", base).Count(&out.Total).Error; err != nil {
		return
	}
	ordered := db.Table("(?) matched", base)
	order := "heat DESC,name,id"
	if q.Sort == "heat_asc" {
		order = "heat ASC,name,id"
	}
	if rank && q.Q != "" {
		// 一次构造完整 ORDER BY，避免后续 Order 调用覆盖参数化匹配表达式。
		ordered = ordered.Order(clause.OrderBy{Expression: clause.Expr{
			SQL:  "CASE WHEN lower(name)=lower(?) THEN 0 WHEN left(lower(name),char_length(?))=lower(?) THEN 1 ELSE 2 END," + order,
			Vars: []any{q.Q, q.Q, q.Q},
		}})
	} else {
		ordered = ordered.Order(order)
	}
	err = ordered.Limit(q.Limit).Offset((q.Page - 1) * q.Limit).Scan(&out.Items).Error
	return
}

// ReadTraits 返回运营权限下的统一筛选列表。
func (s *Service) ReadTraits(ctx context.Context, token, rid string, q TraitQuery) (out TraitPage, err error) {
	defer s.done("traits_read", rid, time.Now(), &err)
	q, err = normalizeTraitQuery(q, 50)
	if err != nil {
		return
	}
	err = s.read(ctx, token, "", true, func(db *gorm.DB, _ reader) error { out, err = traitPage(db, q, false); return err })
	return
}

// ReadTraitCloud 返回与列表一致的聚合，最多绘制一百个词。
func (s *Service) ReadTraitCloud(ctx context.Context, token, rid string, q TraitQuery) (out TraitCloud, err error) {
	defer s.done("traits_cloud", rid, time.Now(), &err)
	out.Items = []TraitWord{}
	q, err = normalizeTraitQuery(q, 100)
	if err != nil {
		return
	}
	q.Page = 1
	err = s.read(ctx, token, "", true, func(db *gorm.DB, _ reader) error {
		page, e := traitPage(db, q, false)
		if e != nil {
			return e
		}
		out.Total = page.Total
		for _, row := range page.Items {
			out.Items = append(out.Items, TraitWord{ID: row.ID, Name: row.Name, Heat: row.Heat})
		}
		return nil
	})
	return
}

// SuggestTraits 只向已登录学生推荐已通过词条。
func (s *Service) SuggestTraits(ctx context.Context, token, rid, query, purpose string, limit int) (out TraitCloud, err error) {
	defer s.done("traits_suggest", rid, time.Now(), &err)
	out.Items = []TraitWord{}
	if purpose == "" {
		purpose = "ideal"
	}
	if !slices.Contains([]string{"personal", "ideal"}, purpose) {
		return out, command.ErrInvalid
	}
	q, e := normalizeTraitQuery(TraitQuery{Q: query, Purpose: purpose, Limit: limit}, 20)
	if e != nil {
		return out, e
	}
	err = s.read(ctx, token, "", false, func(db *gorm.DB, _ reader) error {
		page, e := traitPage(db, q, true)
		if e != nil {
			return e
		}
		out.Total = page.Total
		for _, row := range page.Items {
			out.Items = append(out.Items, TraitWord{ID: row.ID, Name: row.Name, Heat: row.Heat})
		}
		return nil
	})
	return
}

// ReadTrait 读取单个审核案件，证据绝不在学生推荐接口返回。
func (s *Service) ReadTrait(ctx context.Context, token, rid, id string) (out TraitDetail, err error) {
	defer s.done("trait_read", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, "", true, func(db *gorm.DB, _ reader) error {
		var row entity.MatchmakingTrait
		if e := db.Where("id=?", id).Take(&row).Error; e != nil {
			return e
		}
		if e := db.Table("(?) matched", traitAggregate(db, TraitQuery{Status: row.Status})).Where("id=?", id).Scan(&out.Trait).Error; e != nil {
			return e
		}
		out.Evidence, out.ReviewedAt, out.ReviewedBy = row.Evidence, row.ReviewedAt, row.ReviewedBy
		return nil
	})
	return
}
