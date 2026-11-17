package verification

import (
	"context"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// History 读取当前终态申请的安全投影，每次复核岗位、账号生命周期并提交独立访问留迹。
// 历史结果不依赖学生卡仍可读取，也不提供打开材料或再次审核所需的关联引用。
func (s *Service) History(ctx context.Context, token, rid, id string) (out HistoryCase, err error) {
	defer func() {
		if err != nil {
			out = HistoryCase{}
		}
	}()
	defer s.done("history", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	if err = s.transaction(ctx, func(db *gorm.DB) error {
		_, failure := s.authorize(db.Statement.Context, db, token, "admin", command.Discovery{})
		return failure
	}); err != nil {
		return out, err
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		initial, failure := review(db, id)
		if failure != nil {
			return failure
		}
		actor, failure := s.authorize(db.Statement.Context, db, token, "admin", command.Discovery{
			Accounts:  []string{initial.AccountID},
			Resources: []command.Resource{{Kind: command.IdentityReview, ID: id}},
		})
		if failure != nil {
			return failure
		}
		current, failure := review(db, id)
		if failure != nil {
			return failure
		}
		if current.AccountID != initial.AccountID {
			return command.ErrConflict
		}
		// 重提、注销及材料清理会让当前记录失去终态可读资格；不能返回旧的缓存结果。
		if len(current.DataCipher) == 0 || !slices.Contains([]string{"approved", "rejected"}, current.Status) {
			return command.ErrNotFound
		}
		material, failure := s.decrypt(db.Statement.Context, current)
		if failure != nil {
			return failure
		}
		if failure = audit(db, actor, current.AccountID, "identity_review.history", "allowed"); failure != nil {
			return failure
		}
		out = HistoryCase{
			ID: current.ID, Version: current.Version, Status: current.Status,
			Name: material.Name, StudentNumber: material.StudentNumber,
			CreatedAt: current.CreatedAt, ReviewedAt: current.ReviewedAt, ReviewerID: current.ReviewerID,
		}
		if current.Status == "rejected" {
			out.Reason = current.Reason
			out.RevisionSuggestion = current.RevisionSuggestion
		}
		return nil
	})
	return out, err
}
