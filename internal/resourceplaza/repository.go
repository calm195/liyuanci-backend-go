package resourceplaza

import (
	"errors"
	"slices"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// category 及本文件的存取函数借用已携带请求Context的事务，不另开连接或提交事务。
func category(db *gorm.DB, id string) (c entity.ResourceCategory, err error) {
	err = db.Where("id=?", id).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

// material 将不可见资料统一映射为不存在，避免学生通过对象ID探测草稿或屏障内容。
func material(db *gorm.DB, id string, a actor) (r entity.ResourceMaterial, err error) {
	q := db.Where("id=? AND NOT privacy_blocked", id)
	if !a.Admin {
		q = q.Where("status='published' AND NOT content_blocked")
	}
	err = q.Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

// summaries 仅汇总当前内容版本、仍活跃账号的反馈，列表以一次查询批量取得统计。
func summaries(db *gorm.DB, rows []entity.ResourceMaterial) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ID)
		out[r.ID] = map[string]any{"content_version": r.ContentVersion, "valid": int64(0), "link_invalid": int64(0), "outdated": int64(0), "incomplete": int64(0), "total": int64(0)}
	}
	if len(ids) == 0 {
		return out, nil
	}
	var counts []struct {
		ResourceID, Kind string
		Count            int64
	}
	e := db.Table("public.resource_feedback f").Select("f.resource_id,f.kind,count(*) AS count").Joins("JOIN public.accounts a ON a.id=f.account_id AND a.lifecycle='active'").Joins("JOIN public.resource_materials r ON r.id=f.resource_id AND r.content_version=f.content_version").Where("f.resource_id IN ?", ids).Group("f.resource_id,f.kind").Scan(&counts).Error
	for _, c := range counts {
		out[c.ResourceID][c.Kind] = c.Count
		out[c.ResourceID]["total"] = out[c.ResourceID]["total"].(int64) + c.Count
	}
	return out, e
}

// lock 在主体授权后按统一顺序取得分类和资料锁；分类发生并发变化时要求重试整个事务。
func (s *Service) lock(db *gorm.DB, op, id string, in Input, a actor) (r entity.ResourceMaterial, c entity.ResourceCategory, err error) {
	// 分类集合技术锁仅用于分类创建/改名，先于类别行锁，保护唯一名称错误的稳定映射。
	if op == "category_create" || op == "category_update" {
		if err = db.Exec("SELECT pg_advisory_xact_lock(624109180)").Error; err != nil {
			return
		}
	}
	refs := []command.Resource{}
	if op == "category_update" {
		refs = append(refs, command.Resource{Kind: command.ResourceCategory, ID: id})
	}
	if op == "create" || op == "update" {
		refs = append(refs, command.Resource{Kind: command.ResourceCategory, ID: in.CategoryID})
	}
	if slices.Contains([]string{"update", "publish", "offline", "feedback"}, op) {
		r, err = material(db, id, a)
		if err != nil {
			return
		}
		refs = append(refs, command.Resource{Kind: command.ResourceCategory, ID: r.CategoryID}, command.Resource{Kind: command.ResourceMaterial, ID: r.ID})
	}
	if len(refs) > 0 {
		_, err = command.LockPlan(db.Statement.Context, db, command.Discovery{Resources: refs}, s.cfg.Command.LockTimeout)
		if err != nil {
			if errors.Is(err, command.ErrNotFound) && (op == "create" || op == "update") {
				err = command.ErrConflict
			}
			return
		}
	}
	if op == "category_update" {
		c, err = category(db, id)
		return
	}
	if r.ID != "" {
		old := r.CategoryID
		r, err = material(db, id, a)
		if err != nil {
			return
		}
		if old != r.CategoryID {
			err = command.ErrRetry
			return
		}
		c, err = category(db, r.CategoryID)
		if err == nil && !c.Enabled {
			err = command.ErrUnavailable
		}
	}
	return
}

func (s *Service) audit(db *gorm.DB, a actor, op, id string, version int64, reason string) error {
	r := entity.ResourceAudit{ID: uuid.NewString(), ActorID: a.ID, TargetID: id, Operation: op, Version: version, CreatedAt: a.Now}
	if reason != "" {
		b, e := s.encrypt(db.Statement.Context, "resource_offline_reason", r.ID, version, reason)
		if e != nil {
			return e
		}
		r.ReasonCipher = b
	}
	return db.Create(&r).Error
}

// clearFeedback 与内容更新共用事务，同时移除旧反馈及其回执，防止重放恢复过期反馈。
func clearFeedback(db *gorm.DB, id string) error {
	if e := db.Exec("DELETE FROM public.command_receipts WHERE actor_kind='student' AND result_ref IN (SELECT id FROM public.resource_feedback WHERE resource_id=?)", id).Error; e != nil {
		return e
	}
	return db.Where("resource_id=?", id).Delete(&entity.ResourceFeedback{}).Error
}
