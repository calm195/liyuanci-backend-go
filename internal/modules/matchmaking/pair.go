package matchmaking

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func (s *Service) candidateProfile(ctx context.Context, db *gorm.DB, a, id string, now time.Time) (entity.MatchmakingEnrollment, Profile, error) {
	row, err := enrollment(db, a, id)
	if err != nil {
		return row, Profile{}, err
	}
	if row.Status != "approved" {
		return row, Profile{}, command.ErrConflict
	}
	ok, err := eligible(db, row.AccountID, now)
	if err != nil {
		return row, Profile{}, err
	}
	if !ok {
		return row, Profile{}, command.ErrConflict
	}
	ok, err = unused(db, row.ID)
	if err != nil {
		return row, Profile{}, err
	}
	if !ok {
		return row, Profile{}, command.ErrConflict
	}
	p, err := s.decrypt(ctx, row)
	return row, p, err
}

// SetPair 只创建待发布关系，双方唯一占用、评分与审计在同一事务中落库。
func (s *Service) SetPair(ctx context.Context, token, key, rid, id string, input PairInput) (Receipt, error) {
	if !validID(id) || !validID(input.First) || !validID(input.Second) || input.First == input.Second || input.ExpectedVersion < 1 {
		return Receipt{}, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, id, "pair", "POST", "/api/v1/admin/matchmaking/activities/"+id+"/pairs", input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		if a.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if a.Status != "published" || tx.Now().Before(a.RegistrationClosesAt) || !tx.Now().Before(a.EndsAt) {
			return "", command.ErrConflict
		}
		first, p, err := s.candidateProfile(ctx, tx.DB(), id, input.First, tx.Now())
		if err != nil {
			return "", err
		}
		second, q, err := s.candidateProfile(ctx, tx.DB(), id, input.Second, tx.Now())
		if err != nil {
			return "", err
		}
		if first.AccountID == second.AccountID || p.Gender == q.Gender {
			return "", command.ErrConflict
		}
		no, err := blocked(tx.DB(), first.AccountID, second.AccountID)
		if err != nil {
			return "", err
		}
		if no {
			return "", command.ErrBlockedInteraction
		}
		score, details := Score(p, q)
		raw, err := json.Marshal(details)
		if err != nil {
			return "", err
		}
		pair := entity.MatchmakingPair{ID: uuid.NewString(), ActivityID: id, Version: 1, Status: "draft", Score: score, ScoreDetails: raw, Algorithm: "traits-bidirectional-v1", CreatedAt: tx.Now()}
		if err = tx.DB().Create(&pair).Error; err != nil {
			return "", err
		}
		for _, row := range []entity.MatchmakingEnrollment{first, second} {
			member := entity.MatchmakingMember{
				PairID:         pair.ID,
				EnrollmentID:   row.ID,
				ActivityID:     id,
				AccountID:      row.AccountID,
				ProfileVersion: row.ProfileVersion,
				Active:         true,
			}
			if err = tx.DB().Create(&member).Error; err != nil {
				return "", err
			}
		}
		if err = bump(tx.DB(), id); err != nil {
			return "", err
		}
		return pair.ID, audit(tx.DB(), id, tx.Actor().ID, pair.ID, "pair", "", tx.Now())
	}, nil)
}

func endPair(tx *command.Tx, p entity.MatchmakingPair, reason string) error {
	if p.Status == "ended" {
		return command.ErrConflict
	}
	members, err := pairMembers(tx.DB(), p.ID)
	if err != nil {
		return err
	}
	if err = tx.DB().Model(&p).Updates(map[string]any{"status": "ended", "version": p.Version + 1, "ended_at": tx.Now(), "reason": reason}).Error; err != nil {
		return err
	}
	if err = tx.DB().Model(&entity.MatchmakingMember{}).Where("pair_id=?", p.ID).Update("active", false).Error; err != nil {
		return err
	}
	for _, m := range members {
		if err = notify(tx.DB(), m.AccountID, "matchmaking_ended", p.ID, tx.Now()); err != nil {
			return err
		}
	}
	return nil
}

// EndPair 管理员解除关系后立即释放双方名额，保留独立报名和可审计历史。
func (s *Service) EndPair(ctx context.Context, token, key, rid, id, pairID string, input VersionInput) (Receipt, error) {
	if !validID(id) || !validID(pairID) || input.ExpectedVersion < 1 || !text(input.Reason, 200) {
		return Receipt{}, command.ErrInvalid
	}
	path := "/api/v1/admin/matchmaking/activities/" + id + "/pairs/" + pairID + "/end"
	return s.write(ctx, token, key, rid, id, "pair_end", "POST", path, input, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		var p entity.MatchmakingPair
		if err := tx.DB().Where("id=? AND activity_id=?", pairID, id).Take(&p).Error; err != nil {
			return "", err
		}
		if p.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if err := endPair(tx, p, input.Reason); err != nil {
			return "", err
		}
		if err := bump(tx.DB(), id); err != nil {
			return "", err
		}
		return p.ID, audit(tx.DB(), id, tx.Actor().ID, p.ID, "pair_end", input.Reason, tx.Now())
	}, nil)
}

func (s *Service) publishResults(ctx context.Context, tx *command.Tx, a entity.MatchmakingActivity) error {
	list, err := resultTemplates(tx.DB(), a)
	if err != nil {
		return err
	}
	for _, v := range list {
		if err = s.cfg.CheckText(ctx, tx.DB(), v.Body); err != nil {
			return err
		}
	}
	pairs := []entity.MatchmakingPair{}
	if err = tx.DB().Where("activity_id=? AND status='draft'", a.ID).Order("id").Find(&pairs).Error; err != nil {
		return err
	}
	if len(pairs) == 0 {
		return command.ErrConflict
	}
	// 同一事务逐对复核并生成快照；任意一对失效会撤销本轮所有任务和发布标记。
	for _, p := range pairs {
		members, e := pairMembers(tx.DB(), p.ID)
		if e != nil {
			return e
		}
		ok, e := pairEligible(tx.DB(), members, tx.Now())
		if e != nil {
			return e
		}
		if !ok {
			return command.ErrConflict
		}
		for _, m := range members {
			row, e := enrollment(tx.DB(), a.ID, m.EnrollmentID)
			if e != nil {
				return e
			}
			profile, e := s.decrypt(ctx, row)
			if e != nil {
				return e
			}
			if e = s.cfg.CheckText(ctx, tx.DB(), profile.Name+"\n"+profile.Introduction); e != nil {
				return e
			}
		}
		tasks, e := assignTasks(a, p.ID, list)
		if e != nil {
			return e
		}
		if e = tx.DB().Create(&tasks).Error; e != nil {
			return e
		}
		if e = tx.DB().Model(&p).Updates(map[string]any{"status": "published", "published_at": tx.Now(), "version": p.Version + 1}).Error; e != nil {
			return e
		}
		conversation := entity.Conversation{ID: uuid.NewString(), MatchmakingPairID: &p.ID, CreatedAt: tx.Now()}
		if e = tx.DB().Create(&conversation).Error; e != nil {
			return e
		}
		for _, m := range members {
			if e = notify(tx.DB(), m.AccountID, "matchmaking_published", p.ID, tx.Now()); e != nil {
				return e
			}
		}
	}
	// 首次任务发布标记与全部快照同事务提交；注销删除个人关系也不解除题库冻结。
	if a.TasksPublishedAt == nil {
		return tx.DB().Model(&entity.MatchmakingActivity{}).Where("id=?", a.ID).
			UpdateColumn("tasks_published_at", tx.Now()).Error
	}
	return nil
}
