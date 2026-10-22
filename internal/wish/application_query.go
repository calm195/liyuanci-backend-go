package wish

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func (s *Service) applicationResult(ctx context.Context, db *gorm.DB, row entity.Application, now time.Time, ownerView bool) (out ApplicationResult, err error) {
	out = ApplicationResult{ID: row.ID, Version: row.Version, WishID: row.WishID, Status: row.Status}
	if row.Status == "pending_scan" || row.Status == "pending_review" || row.Status == "active" {
		ok, e := ApplicationEligible(ctx, db, row, now)
		if e != nil {
			return out, e
		}
		if !ok {
			out.Status = "invalidated"
			return out, nil
		}
	}
	if row.ReviewTaskID != nil {
		var task entity.ModerationTask
		if e := db.WithContext(ctx).Where("id=?", *row.ReviewTaskID).Take(&task).Error; e != nil {
			return out, e
		}
		decision := "pending"
		if task.Status == "approved" || task.Status == "rejected" {
			decision = task.Status
		}
		if !ownerView {
			out.ReviewResult = &ReviewResult{Decision: decision, ContentVersion: row.ContentVersion}
			if decision == "rejected" {
				out.ReviewResult.Reason = task.Reason
				out.ReviewResult.RevisionSuggestion = task.RevisionSuggestion
			}
		}
	}
	if ownerView {
		if out.Status != "active" {
			return ApplicationResult{}, command.ErrNotFound
		}
		message, e := applicationMessage(ctx, s.codec, row)
		if e != nil {
			return out, e
		}
		var actor entity.Account
		if e = db.WithContext(ctx).Where("id=? AND lifecycle='active'", row.ApplicantID).Take(&actor).Error; e != nil {
			return out, e
		}
		// 申请不收身份标签；公开主页投影只给昵称和不透明账号标识，禁止从认证材料补全标签。
		out.Applicant = &PublicAuthor{ID: actor.ID, Nickname: actor.Nickname, IdentityTags: IdentityTags{}}
		out.Message = &message
	}
	return out, nil
}

// OwnApplications 返回本人申请状态与驳回建议，不展示待审留言或他人私有心愿正文。
func (s *Service) OwnApplications(ctx context.Context, token, rid string, q ApplicationQuery) (ApplicationPage, error) {
	return s.applications(ctx, token, rid, "", q)
}

// Applications 仅许愿人可读，过滤过期、旧内容、撤权、拉黑和屏障后的申请，不以陈旧active字段授权。
func (s *Service) Applications(ctx context.Context, token, rid, id string, q ApplicationQuery) (ApplicationPage, error) {
	if !validWishID(id) {
		return ApplicationPage{}, command.ErrInvalid
	}
	return s.applications(ctx, token, rid, id, q)
}
func (s *Service) applications(ctx context.Context, token, rid, id string, q ApplicationQuery) (out ApplicationPage, err error) {
	defer func() {
		if err != nil {
			out = ApplicationPage{}
		}
	}()
	defer s.interactionDone("application.list", rid, time.Now(), &err)
	if q.Limit < 1 || q.Limit > 100 || q.Status != "" && (!validApplicationStatus(q.Status) || id != "") {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		ctx := db.Statement.Context
		actor, e := currentReader(db.Statement.Context, db, token, now)
		if e != nil {
			return e
		}
		if id != "" {
			a, e := readAggregate(ctx, db, id, false)
			if e != nil {
				return e
			}
			if a.row.OwnerID != actor {
				return command.ErrNotFound
			}
			if !ordinary(a.owner, a.snapshot) {
				return command.ErrForbidden
			}
		}
		scope := "applications|" + actor + "|" + id + "|" + q.Status
		cursor, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		out.Items = []ApplicationResult{}
		var positions []string
		for len(out.Items) <= q.Limit {
			query := db.Model(&entity.Application{})
			if id == "" {
				query = query.Where("applicant_id=?", actor)
			} else {
				query = query.Where("wish_id=? AND status='active'", id)
			}
			if q.Status == "invalidated" {
				query = query.Where("status IN ?", []string{"invalidated", "pending_scan", "pending_review", "active"})
			} else if q.Status != "" {
				query = query.Where("status=?", q.Status)
			}
			if cursor.ID != "" {
				query = query.Where("id<?", cursor.ID)
			}
			var rows []entity.Application
			if e = query.Order("id DESC").Limit(100).Find(&rows).Error; e != nil {
				return e
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				cursor = pageCursor{Scope: scope, ID: row.ID}
				result, e := s.applicationResult(ctx, db, row, now, id != "")
				if errors.Is(e, command.ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				if q.Status != "" && result.Status != q.Status {
					continue
				}
				out.Items = append(out.Items, result)
				positions = append(positions, row.ID)
				if len(out.Items) > q.Limit {
					break
				}
			}
			if len(rows) < 100 {
				break
			}
		}
		if len(out.Items) > q.Limit {
			next := s.encodeCursor(pageCursor{Scope: scope, ID: positions[q.Limit-1]})
			out.NextCursor = &next
			out.Items = out.Items[:q.Limit]
		}
		return nil
	})
	if err != nil {
		err = readError(err)
	}
	return
}
