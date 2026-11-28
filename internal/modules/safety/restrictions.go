package safety

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// ReportContentControl 是举报处置前的最小投影，版本来自写命令使用的对象锁快照。
type ReportContentControl struct {
	ReportID       string `json:"report_id"`
	ReportVersion  int64  `json:"report_version"`
	ReportStatus   string `json:"report_status"`
	TargetKind     string `json:"target_kind"`
	TargetID       string `json:"target_id"`
	ContentRef     string `json:"content_ref"`
	ContentVersion int64  `json:"content_version"`
	Version        int64  `json:"version"`
}

// 心愿与还愿故事共享心愿对象的版本，其余内容使用各自对象的版本。
func restrictionResource(kind, id string) command.Resource {
	var resource command.ResourceKind
	switch kind {
	case "wish", "story":
		resource = command.Wish
	case "wish_comment":
		resource = command.WishComment
	case "application":
		resource = command.Application
	case "message":
		resource = command.Message
	}
	return command.Resource{Kind: resource, ID: id}
}

func matchRestrictionReport(db *gorm.DB, r entity.Report, c entity.ContentRegistry) error {
	if r.ContentID == nil || r.TargetKind != c.Kind || r.TargetID != c.EntityID ||
		!slices.Contains([]string{"pending", "processing", "resolved", "rejected"}, r.Status) {
		return command.ErrNotFound
	}
	if e := ordinaryCase(db, *r.ContentID); e != nil {
		return e
	}
	if *r.ContentID != c.ID {
		// 举报绑定其提交时的正文，换版后须核对当前案件，不能沿用旧案处置新稿。
		return command.ErrVersionConflict
	}
	return nil
}

func (s *Service) readReportContentControl(db *gorm.DB, id string) (out ReportContentControl, err error) {
	r, e := report(db, id)
	if e != nil {
		return out, e
	}
	ref := restrictionResource(r.TargetKind, r.TargetID)
	if r.ContentID == nil || ref.Kind == "" {
		return out, command.ErrNotFound
	}
	c, e := targetRegistry(db, r.TargetKind, r.TargetID)
	if e != nil {
		return out, e
	}
	if e = matchRestrictionReport(db, r, c); e != nil {
		return out, e
	}
	ctx := db.Statement.Context
	plan, e := contentPlan(ctx, db, c)
	if e != nil {
		return out, e
	}
	plan.Resources = append(plan.Resources, command.Resource{Kind: command.Report, ID: id})
	tx, e := command.LockPlan(ctx, db, plan, s.cfg.Command.LockTimeout)
	if e != nil {
		return out, e
	}
	current, ok := tx.Resource(ref)
	if !ok || current.Version < 1 {
		return out, command.ErrNotFound
	}
	return ReportContentControl{
		ReportID:       r.ID,
		ReportVersion:  r.Version,
		ReportStatus:   r.Status,
		TargetKind:     c.Kind,
		TargetID:       c.EntityID,
		ContentRef:     c.ID,
		ContentVersion: c.ContentVersion,
		Version:        current.Version,
	}, nil
}

// restrict 只处理普通违规限制，解除操作不改变业务状态、危机原因或隐私屏障。
func (s *Service) restrict(ctx context.Context, tx *command.Tx, op, id string, in Input) (string, error) {
	db := tx.DB().WithContext(ctx)
	if in.ReportID != "" {
		// 在通过授权的同一写事务核对举报快照；幂等回执重放走原读取流程。
		current, ok := tx.Resource(command.Resource{Kind: command.Report, ID: in.ReportID})
		if !ok {
			return "", command.ErrNotFound
		}
		if current.Version != in.ReportVersion {
			return "", command.ErrVersionConflict
		}
	}
	c, e := targetRegistry(db, in.TargetKind, id)
	if e != nil {
		return "", e
	}
	if op == "release" {
		var rows []entity.ContentRestriction
		if e = db.Table("public.content_restrictions AS r").Select("r.*").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where(
			"c.kind=? AND c.entity_id=? AND r.kind='violation' AND r.active",
			in.TargetKind,
			id,
		).Find(&rows).Error; e != nil {
			return "", e
		}
		for _, r := range rows {
			cipher, e := s.encrypt(ctx, "content_release_reason", r.ID, in.Reason)
			if e != nil {
				return "", e
			}
			if e = db.Model(&entity.ContentRestriction{}).Where("id=?", r.ID).
				Updates(map[string]any{"active": false, "release_reason_cipher": cipher, "version": gorm.Expr("version+1")}).
				Error; e != nil {
				return "", e
			}
		}
	} else {
		r := entity.ContentRestriction{
			ID:        uuid.NewString(),
			ContentID: c.ID,
			Kind:      "violation",
			SourceRef: uuid.NewString(),
			Active:    true,
		}
		r.ReasonCipher, e = s.encrypt(ctx, "content_offline_reason", r.ID, in.Reason)
		if e != nil {
			return "", e
		}
		if e = db.Create(&r).Error; e != nil {
			return "", e
		}
		switch c.Kind {
		case "wish_comment":
			if e = db.Model(&entity.WishComment{}).Where("id=?", id).Update("status", "cancelled").Error; e != nil {
				return "", e
			}
		case "wish":
			if e = db.Model(&entity.Wish{}).Where("id=? AND state IN ('published','ready')", id).Update("state", "withdrawn").Error; e != nil {
				return "", e
			}
		case "story":
			if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", id, c.ContentVersion).Update("public_body", nil).Error; e != nil {
				return "", e
			}
		case "application":
			var a entity.Application
			if e = db.Where("id=?", id).Take(&a).Error; e != nil {
				return "", e
			}
			if a.Status != "accepted" {
				if e = wish.CancelApplication(ctx, db, a, "invalidated", "content_violation"); e != nil {
					return "", e
				}
			}
		case "message":
			var m entity.Message
			if e = db.Where("id=?", id).Take(&m).Error; e != nil {
				return "", e
			}
			if m.Status == "pending_scan" || m.Status == "pending_review" {
				if e = chat.CancelMessage(ctx, db, m); e != nil {
					return "", e
				}
			}
		}
	}
	switch c.Kind {
	case "wish_comment":
		e = db.Model(&entity.WishComment{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error
	case "wish", "story":
		e = db.Model(&entity.Wish{}).Where("id=?", id).Updates(map[string]any{"version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1")}).
			Error
	case "application":
		e = db.Model(&entity.Application{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error
	case "message":
		e = db.Model(&entity.Message{}).Where("id=?", id).Update("version", gorm.Expr("version+1")).Error
	}
	if e != nil {
		return "", e
	}
	return id, audit(db, tx.Actor().ID, c.ID, "content."+op)
}

func restrictionSummary(db *gorm.DB, c entity.ContentRegistry) (Summary, error) {
	out := Summary{ID: c.EntityID, Kind: c.Kind, ContentVersion: c.ContentVersion, Status: "released"}
	var n int64
	if e := db.Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where(
		"c.kind=? AND c.entity_id=? AND r.active",
		c.Kind,
		c.EntityID,
	).Count(&n).Error; e != nil {
		return out, e
	}
	if n > 0 {
		out.Status = "restricted"
	}
	var model any
	switch c.Kind {
	case "wish_comment":
		model = &entity.WishComment{}
	case "wish", "story":
		model = &entity.Wish{}
	case "application":
		model = &entity.Application{}
	case "message":
		model = &entity.Message{}
	}
	e := db.Model(model).Select("version").Where("id=?", c.EntityID).Scan(&out.Version).Error
	return out, e
}
