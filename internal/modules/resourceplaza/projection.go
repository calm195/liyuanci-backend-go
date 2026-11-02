package resourceplaza

import (
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func catProjection(c entity.ResourceCategory, admin bool) map[string]any {
	p := map[string]any{"id": c.ID, "name": c.Name}
	if admin {
		p["sort_order"] = c.SortOrder
		p["enabled"] = c.Enabled
		p["version"] = c.Version
		p["created_at"] = c.CreatedAt.UTC()
		p["updated_at"] = c.UpdatedAt.UTC()
	}
	return p
}

func feedbackProjection(f entity.ResourceFeedback) map[string]any {
	return map[string]any{"resource_id": f.ResourceID, "content_version": f.ContentVersion, "kind": f.Kind, "version": f.Version, "updated_at": f.UpdatedAt.UTC()}
}

func utc(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	t := v.UTC()
	return &t
}

// project 按主体投影字段和可用动作，只有详情才解密下载信息；列表使用批量分类和统计。
// 调用方须先经过transaction授权及material或等价列表可见性过滤。
func (s *Service) project(db *gorm.DB, a actor, r entity.ResourceMaterial, detail bool, stats map[string]map[string]any, categories map[string]entity.ResourceCategory) (map[string]any, error) {
	c, ok := categories[r.CategoryID]
	var e error
	if !ok && categories == nil {
		c, e = category(db, r.CategoryID)
	}
	if e != nil || !c.Enabled {
		return nil, command.ErrUnavailable
	}
	p := map[string]any{"id": r.ID, "title": r.Title, "category": catProjection(c, false), "version": r.Version, "created_at": r.CreatedAt.UTC(), "updated_at": r.UpdatedAt.UTC(), "published_at": utc(r.PublishedAt)}
	if detail {
		d, e := s.download(db.Statement.Context, r)
		if e != nil {
			return nil, e
		}
		p["description"] = r.Description
		p["download_url"] = d.URL
		p["extraction_code"] = d.Code
		p["content_version"] = r.ContentVersion
	} else {
		p["description_excerpt"] = string([]rune(r.Description)[:min(120, len([]rune(r.Description)))])
	}
	if a.Admin {
		p["status"] = r.Status
		p["content_version"] = r.ContentVersion
		p["visibility_blocked"] = r.ContentBlocked
		actions := []string{"update", "publish"}
		if r.ContentBlocked {
			actions = []string{"update"}
		}
		if r.Status == "published" {
			actions = []string{"offline"}
		}
		p["allowed_actions"] = actions
		p["feedback_summary"] = stats[r.ID]
	} else if detail {
		p["my_feedback"] = nil
		var fs []entity.ResourceFeedback
		if e = db.Where("resource_id=? AND account_id=? AND content_version=?", r.ID, a.ID, r.ContentVersion).Find(&fs).Error; e != nil {
			return nil, e
		}
		if len(fs) == 1 {
			p["my_feedback"] = feedbackProjection(fs[0])
		}
		actions := []string{"copy_download", "submit_feedback"}
		if a.Muted {
			actions = actions[:1]
		}
		p["allowed_actions"] = actions
	}
	return p, nil
}
