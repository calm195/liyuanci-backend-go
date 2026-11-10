package matchmaking

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type reader struct {
	ID  string
	Now time.Time
}

func (s *Service) read(ctx context.Context, token, id string, admin bool, run func(*gorm.DB, reader) error) error {
	if token == "" {
		return command.ErrUnauthenticated
	}
	if id != "" && !validID(id) {
		return command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	for attempt := 0; attempt < s.cfg.Command.MaxAttempts; attempt++ {
		err := s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			hash := sha256.Sum256([]byte(token))
			var session entity.Session
			if err := db.Where("token_hash=?", hash[:]).Take(&session).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return command.ErrUnauthenticated
				}
				return err
			}
			if (session.SubjectKind == "admin") != admin {
				return command.ErrForbidden
			}
			plan, err := discover(ctx, db, id)
			if err != nil {
				return err
			}
			if admin {
				plan.Admins = append(plan.Admins, session.SubjectID)
			} else {
				plan.Accounts = append(plan.Accounts, session.SubjectID)
			}
			locked, err := command.LockPlan(ctx, db, plan, s.cfg.Command.LockTimeout)
			if err != nil {
				return err
			}
			var epoch int64
			if admin {
				var a entity.AdminAccount
				if err = db.Where("id=? AND status='active'", session.SubjectID).Take(&a).Error; err != nil {
					return command.ErrUnauthenticated
				}
				epoch = a.AuthEpoch
				var count int64
				if err = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=? AND role IN ('review_operator','super_admin')", a.ID).Count(&count).Error; err != nil {
					return err
				}
				if count == 0 {
					return command.ErrForbidden
				}
			} else {
				a, ok := locked.Guard(session.SubjectID)
				if !ok || a.Lifecycle != "active" {
					return command.ErrUnauthenticated
				}
				epoch = a.AuthEpoch
			}
			if err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND auth_epoch=? AND revoked_at IS NULL AND expires_at>?", session.ID, epoch, locked.Now()).Take(&session).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return command.ErrUnauthenticated
				}
				return err
			}
			return run(db, reader{ID: session.SubjectID, Now: locked.Now()})
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		err = safe(err)
		if !errors.Is(err, command.ErrRetry) {
			return err
		}
	}
	return command.ErrRetry
}

func projectActivity(row entity.MatchmakingActivity, now time.Time) Activity {
	campuses := []string{}
	_ = json.Unmarshal(row.Campuses, &campuses)
	phase := row.Status
	if row.Status == "published" {
		switch {
		case now.Before(row.RegistrationOpensAt):
			phase = "upcoming"
		case now.Before(row.RegistrationClosesAt):
			phase = "registration"
		case now.Before(row.StartsAt):
			phase = "matching"
		case now.Before(row.EndsAt):
			phase = "active"
		default:
			phase = "ended"
		}
	}
	return Activity{
		ActivityInput: ActivityInput{
			Title:                row.Title,
			Description:          row.Description,
			Campuses:             campuses,
			RegistrationOpensAt:  row.RegistrationOpensAt,
			RegistrationClosesAt: row.RegistrationClosesAt,
			ResultsAt:            row.ResultsAt,
			StartsAt:             row.StartsAt,
			EndsAt:               row.EndsAt,
			Days:                 row.Days,
		},
		ID:      row.ID,
		Version: row.Version,
		Status:  row.Status,
		Phase:   phase,
	}
}

func (s *Service) projectEnrollment(ctx context.Context, db *gorm.DB, row entity.MatchmakingEnrollment, now time.Time) (Enrollment, error) {
	out := Enrollment{ID: row.ID, Version: row.Version, Status: row.Status, Reason: row.Reason}
	var count int64
	if err := db.Model(&entity.Account{}).Where("id=? AND lifecycle='active'", row.AccountID).Count(&count).Error; err != nil {
		return out, err
	}
	if count == 0 {
		return out, nil
	}
	p, err := s.decrypt(ctx, row)
	if err != nil {
		return out, err
	}
	out.Profile = &p
	ok, err := eligible(db, row.AccountID, now)
	if err != nil {
		return out, err
	}
	free, err := unused(db, row.ID)
	out.Eligible = ok && free && row.Status == "approved"
	return out, err
}

func (s *Service) projectPair(ctx context.Context, db *gorm.DB, p entity.MatchmakingPair, now time.Time, admin bool) (Pair, error) {
	out := Pair{ID: p.ID, Version: p.Version, Status: p.Status, Score: p.Score, Algorithm: p.Algorithm, Reason: p.Reason}
	if err := json.Unmarshal(p.ScoreDetails, &out.ScoreDetails); err != nil {
		return out, err
	}
	members, err := pairMembers(db, p.ID)
	if err != nil {
		return out, err
	}
	if admin {
		for _, m := range members {
			out.Enrollments = append(out.Enrollments, m.EnrollmentID)
			e, err := enrollment(db, p.ActivityID, m.EnrollmentID)
			if err != nil {
				return out, err
			}
			v, err := s.projectEnrollment(ctx, db, e, now)
			if err != nil {
				return out, err
			}
			name := "资料不可访问"
			if v.Profile != nil {
				name = v.Profile.Name
			}
			out.MemberNames = append(out.MemberNames, name)
		}
	}
	return out, nil
}

// List 返回同一快照中的活动页，学生仅可见曾发布的活动。
func (s *Service) List(ctx context.Context, token, rid string, admin bool, page, limit int) (out Page, err error) {
	defer s.done("list", rid, time.Now(), &err)
	if page < 1 || page > 10000 || limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, "", admin, func(db *gorm.DB, r reader) error {
		out = Page{Items: []Activity{}}
		q := db.Model(&entity.MatchmakingActivity{})
		if !admin {
			q = q.Where("published_at IS NOT NULL")
		}
		if e := q.Count(&out.Total).Error; e != nil {
			return e
		}
		rows := []entity.MatchmakingActivity{}
		if e := q.Order("registration_opens_at DESC,id").Limit(limit).Offset((page - 1) * limit).Find(&rows).Error; e != nil {
			return e
		}
		for _, v := range rows {
			out.Items = append(out.Items, projectActivity(v, r.Now))
		}
		if admin {
			return nil
		}
		return loadEndedSummaries(db, out.Items)
	})
	if err != nil {
		out = Page{}
	}
	return out, err
}

// Read 读取本人状态，未发布关系和无权限的对方资料不会回传客户端。
func (s *Service) Read(ctx context.Context, token, rid, id string) (out Detail, err error) {
	defer s.done("detail", rid, time.Now(), &err)
	err = s.read(ctx, token, id, false, func(db *gorm.DB, r reader) error {
		out = Detail{Tasks: []Task{}}
		a, e := activity(db, id)
		if e != nil {
			return e
		}
		if a.PublishedAt == nil {
			return command.ErrNotFound
		}
		activities := []Activity{projectActivity(a, r.Now)}
		if e = loadEndedSummaries(db, activities); e != nil {
			return e
		}
		out.Activity = activities[0]
		var row entity.MatchmakingEnrollment
		e = db.Where("activity_id=? AND account_id=?", id, r.ID).Take(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		projected, e := s.projectEnrollment(ctx, db, row, r.Now)
		if e != nil {
			return e
		}
		out.Enrollment = &projected
		var p entity.MatchmakingPair
		e = db.Where("activity_id=? AND status<>'draft' AND id IN (SELECT pair_id FROM public.matchmaking_members WHERE account_id=?)", id, r.ID).Order("created_at DESC,id").Take(&p).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		v, e := s.projectPair(ctx, db, p, r.Now, false)
		if e != nil {
			return e
		}
		// 解除原因只显示通用提示，避免向对方泄露运营记录。
		if p.Status == "ended" {
			v.Reason = "配对已解除"
		}
		out.Pair = &v
		members, e := pairMembers(db, p.ID)
		if e != nil {
			return e
		}
		ok, e := pairEligible(db, members, r.Now)
		if e != nil {
			return e
		}
		if !ok || p.Status != "published" || a.Status != "published" {
			return nil
		}
		var conversation entity.Conversation
		e = db.Select("id").Where("matchmaking_pair_id=?", p.ID).Take(&conversation).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		out.Pair.ConversationID = conversation.ID
		if r.Now.Before(a.StartsAt) {
			return nil
		}
		out.Pair.Accessible = true
		for _, m := range members {
			if m.AccountID == r.ID {
				continue
			}
			peer, e := enrollment(db, id, m.EnrollmentID)
			if e != nil {
				return e
			}
			profile, e := s.decrypt(ctx, peer)
			if e != nil {
				return e
			}
			out.Pair.Peer = &profile
		}
		tasks := []entity.MatchmakingTask{}
		if e = db.Where("pair_id=?", p.ID).Order("day_index").Find(&tasks).Error; e != nil {
			return e
		}
		opens := []entity.MatchmakingOpen{}
		if e = db.Where("account_id=? AND task_id IN (SELECT id FROM public.matchmaking_tasks WHERE pair_id=?)", r.ID, p.ID).Find(&opens).Error; e != nil {
			return e
		}
		opened := map[string]time.Time{}
		for _, v := range opens {
			opened[v.TaskID] = v.OpenedAt
		}
		for _, v := range tasks {
			t := Task{ID: v.ID, DayIndex: v.DayIndex, UnlocksAt: v.UnlocksAt}
			if !r.Now.Before(v.UnlocksAt) {
				t.Kind, t.Body = v.Kind, v.Body
				t.CanOpen = r.Now.Before(a.EndsAt)
			}
			if at, ok := opened[v.ID]; ok {
				t.OpenedAt = &at
			}
			out.Tasks = append(out.Tasks, t)
		}
		return nil
	})
	if err != nil {
		out = Detail{}
	}
	return out, err
}

// ReadAdmin 分页读取报名和配对，模板冻结标记不随个人配对的清理消失。
func (s *Service) ReadAdmin(ctx context.Context, token, rid, id string, page, limit int) (out Workspace, err error) {
	defer s.done("workspace", rid, time.Now(), &err)
	if page < 1 || page > 10000 || limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, id, true, func(db *gorm.DB, r reader) error {
		out = Workspace{Enrollments: []Enrollment{}, Pairs: []Pair{}, Templates: []Template{}}
		a, e := activity(db, id)
		if e != nil {
			return e
		}
		out.Activity = projectActivity(a, r.Now)
		q := db.Model(&entity.MatchmakingEnrollment{}).Where("activity_id=?", id)
		if e = q.Count(&out.Total).Error; e != nil {
			return e
		}
		rows := []entity.MatchmakingEnrollment{}
		if e = q.Order("created_at,id").Limit(limit).Offset((page - 1) * limit).Find(&rows).Error; e != nil {
			return e
		}
		for _, row := range rows {
			v, e := s.projectEnrollment(ctx, db, row, r.Now)
			if e != nil {
				return e
			}
			out.Enrollments = append(out.Enrollments, v)
			if v.Profile != nil {
				if e = recordAccess(db, r.ID, row.ID, "matchmaking.enrollment", r.Now); e != nil {
					return e
				}
			}
		}
		pq := db.Model(&entity.MatchmakingPair{}).Where("activity_id=?", id)
		if e = pq.Count(&out.PairTotal).Error; e != nil {
			return e
		}
		pairs := []entity.MatchmakingPair{}
		if e = pq.Order("created_at DESC,id").Limit(limit).Offset((page - 1) * limit).Find(&pairs).Error; e != nil {
			return e
		}
		for _, row := range pairs {
			v, e := s.projectPair(ctx, db, row, r.Now, true)
			if e != nil {
				return e
			}
			out.Pairs = append(out.Pairs, v)
			if e = recordAccess(db, r.ID, row.ID, "matchmaking.pair", r.Now); e != nil {
				return e
			}
		}
		out.TemplatesLocked = a.TasksPublishedAt != nil
		if a.FirstTaskID != nil {
			out.TaskPlan, e = readTaskPlan(db, a)
			return e
		}
		out.Templates, e = templates(db, id)
		return e
	})
	if err != nil {
		out = Workspace{}
	}
	return out, err
}

// Candidates 仅返回仍合格、未占用且双方未拉黑的异性候选，稳定排序后最多50条。
func (s *Service) Candidates(ctx context.Context, token, rid, id, enrollmentID string) (out []Candidate, err error) {
	defer s.done("candidates", rid, time.Now(), &err)
	if !validID(enrollmentID) {
		return nil, command.ErrInvalid
	}
	err = s.read(ctx, token, id, true, func(db *gorm.DB, r reader) error {
		out = []Candidate{}
		a, e := activity(db, id)
		if e != nil {
			return e
		}
		if a.Status != "published" || !r.Now.Before(a.EndsAt) {
			return command.ErrConflict
		}
		first, p, e := s.candidateProfile(ctx, db, id, enrollmentID, r.Now)
		if e != nil {
			return e
		}
		rows := []entity.MatchmakingEnrollment{}
		if e = db.Where("activity_id=? AND status='approved' AND id<>?", id, enrollmentID).Find(&rows).Error; e != nil {
			return e
		}
		for _, row := range rows {
			v, e := s.projectEnrollment(ctx, db, row, r.Now)
			if e != nil {
				return e
			}
			if !v.Eligible || v.Profile == nil || p.Gender == v.Profile.Gender {
				continue
			}
			no, e := blocked(db, first.AccountID, row.AccountID)
			if e != nil {
				return e
			}
			if no {
				continue
			}
			score, details := Score(p, *v.Profile)
			out = append(out, Candidate{Enrollment: v, Score: score, ScoreDetails: details})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Score == out[j].Score {
				return out[i].Enrollment.ID < out[j].Enrollment.ID
			}
			return out[i].Score > out[j].Score
		})
		if len(out) > 50 {
			out = out[:50]
		}
		if e = recordAccess(db, r.ID, first.ID, "matchmaking.candidates", r.Now); e != nil {
			return e
		}
		for _, candidate := range out {
			if e = recordAccess(db, r.ID, candidate.Enrollment.ID, "matchmaking.candidates", r.Now); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		out = nil
	}
	return out, err
}

// NotificationTarget 由通知读取者在其当前会话快照中调用，仅返回本人仍可访问活动的引用。
func NotificationTarget(ctx context.Context, db *gorm.DB, row entity.Notification, now time.Time) (string, string, error) {
	if !slices.Contains([]string{"matchmaking_review", "matchmaking_published", "matchmaking_ended"}, row.Type) {
		return "", "", nil
	}
	var id string
	if row.Type == "matchmaking_review" {
		if err := db.WithContext(ctx).Model(&entity.MatchmakingEnrollment{}).Select("activity_id").Where("id=? AND account_id=?", row.TargetRef, row.RecipientID).Scan(&id).Error; err != nil {
			return "", "", err
		}
	} else {
		if err := db.WithContext(ctx).Model(&entity.MatchmakingMember{}).Select("activity_id").Where("pair_id=? AND account_id=?", row.TargetRef, row.RecipientID).Scan(&id).Error; err != nil {
			return "", "", err
		}
	}
	if id == "" {
		return "", "", nil
	}
	var count int64
	if err := db.WithContext(ctx).Model(&entity.MatchmakingActivity{}).Where("id=? AND published_at IS NOT NULL", id).Count(&count).Error; err != nil {
		return "", "", err
	}
	if count != 1 {
		return "", "", nil
	}
	return "matchmaking", id, nil
}
