package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func (s *Service) write(
	ctx context.Context,
	token, key, rid, id, op, method, path string,
	input any,
	apply func(*command.Tx, entity.MatchmakingActivity) (string, error),
	authorize func(*command.Tx, entity.MatchmakingActivity) error,
) (Receipt, error) {
	student := slices.Contains([]string{"enroll", "withdraw", "task_open", "profile_save"}, op)
	definition := command.Definition{Kind: "matchmaking." + op, Method: method, ResourcePath: path, ActorKind: "admin", AllowedRoles: []string{"review_operator", "super_admin"}, RetainReceipt: true}
	if student {
		definition.ActorKind = "student"
		definition.AllowedRoles = nil
		definition.Access = command.Verified
		if op == "profile_save" {
			definition.Access = command.Ordinary
		}
		if op == "withdraw" {
			definition.Access = command.Restricted
		}
	}
	definition.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		if review, ok := input.(traitReviewCommand); ok {
			plan := command.Discovery{Resources: []command.Resource{{Kind: command.MatchmakingTrait, ID: review.ID}}}
			err := db.WithContext(ctx).Model(&entity.MatchmakingTraitSelection{}).Where("trait_id=?", review.ID).Pluck("account_id", &plan.Accounts).Error
			return plan, err
		}
		return discover(ctx, db, id)
	}
	var current entity.MatchmakingActivity
	definition.Authorize = func(ctx context.Context, tx *command.Tx) error {
		if id != "" {
			var err error
			current, err = activity(tx.DB(), id)
			if err != nil {
				return err
			}
			if student && current.PublishedAt == nil {
				return command.ErrNotFound
			}
		}
		if authorize != nil {
			return safe(authorize(tx, current))
		}
		return nil
	}
	definition.Apply = func(_ context.Context, tx *command.Tx) (string, error) {
		ref, err := apply(tx, current)
		return ref, safe(err)
	}
	definition.Resolve = func(_ context.Context, tx *command.Tx, ref string) error {
		// 回执仅返回技术标识，当前资格仍先经过Authorize，删除后的对象不复活。
		var count int64
		query := tx.DB().Model(&entity.MatchmakingActivity{}).Where("id=?", ref)
		switch op {
		case "profile_save":
			query = tx.DB().Model(&entity.MatchmakingProfile{}).Where("account_id=? AND account_id=?", ref, tx.Actor().ID)
		case "enroll", "withdraw", "review":
			query = tx.DB().Model(&entity.MatchmakingEnrollment{}).Where("id=? AND activity_id=?", ref, id)
			if student {
				query = query.Where("account_id=?", tx.Actor().ID)
			}
		case "pair", "pair_end":
			query = tx.DB().Model(&entity.MatchmakingPair{}).Where("id=? AND activity_id=?", ref, id)
		case "auto_create", "auto_apply":
			query = tx.DB().Model(&entity.MatchmakingAutoJob{}).Where("id=? AND activity_id=?", ref, id)
		case "trait_review":
			query = tx.DB().Model(&entity.MatchmakingTrait{}).Where("id=?", ref)
		case "library_create", "library_save":
			query = tx.DB().Model(&entity.MatchmakingLibraryTask{}).Where("id=?", ref)
		case "task_open":
			query = tx.DB().Model(&entity.MatchmakingTask{}).Where("id=?", ref)
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return command.ErrNotFound
		}
		return nil
	}
	out, err := s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: input})
	if err != nil {
		return Receipt{}, safe(err)
	}
	return Receipt{ID: out.Ref}, nil
}

// SaveActivity 新建或更新草稿，发布后配置冻结。
func (s *Service) SaveActivity(ctx context.Context, token, key, rid, id string, input ActivityInput) (Receipt, error) {
	if !validActivity(input) || id != "" && !validID(id) || id == "" && input.ExpectedVersion != 0 || id != "" && input.ExpectedVersion < 1 {
		return Receipt{}, command.ErrInvalid
	}
	method, path, op := "POST", "/api/v1/admin/matchmaking/activities", "create"
	if id != "" {
		method, path, op = "PUT", path+"/"+id, "save"
	}
	return s.write(ctx, token, key, rid, id, op, method, path, input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		if id != "" && (a.Status != "draft" || a.Version != input.ExpectedVersion) {
			if a.Version != input.ExpectedVersion {
				return "", command.ErrVersionConflict
			}
			return "", command.ErrConflict
		}
		if err := s.cfg.CheckText(ctx, tx.DB(), input.Title+"\n"+input.Description); err != nil {
			return "", err
		}
		var err error
		next := entity.MatchmakingActivity{
			ID:                   id,
			Version:              a.Version + 1,
			Title:                input.Title,
			Description:          input.Description,
			Status:               "draft",
			Campuses:             json.RawMessage(`[]`),
			RegistrationOpensAt:  input.RegistrationOpensAt,
			RegistrationClosesAt: input.RegistrationClosesAt,
			ResultsAt:            input.ResultsAt,
			StartsAt:             input.StartsAt,
			EndsAt:               input.EndsAt,
			Days:                 input.Days,
			CreatedAt:            a.CreatedAt,
			FirstTaskID:          a.FirstTaskID,
		}
		if id == "" {
			next.ID = uuid.NewString()
			next.CreatedAt = tx.Now()
			err = tx.DB().Create(&next).Error
		} else {
			err = tx.DB().Model(&next).Select("*").Updates(&next).Error
		}
		if err != nil {
			return "", err
		}
		return next.ID, audit(tx.DB(), next.ID, tx.Actor().ID, next.ID, op, "", tx.Now())
	}, nil)
}

// ActivityCommand 显式发布、取消活动或原子发布待发布结果。
func (s *Service) ActivityCommand(ctx context.Context, token, key, rid, id, action string, input VersionInput) (Receipt, error) {
	if !validID(id) || input.ExpectedVersion < 1 || len([]rune(input.Reason)) > 200 || !slices.Contains([]string{"publish", "cancel", "results/publish"}, action) || action == "cancel" && !text(input.Reason, 200) {
		return Receipt{}, command.ErrInvalid
	}
	op := action
	if action == "results/publish" {
		op = "results_publish"
	}
	return s.write(ctx, token, key, rid, id, op, "POST", "/api/v1/admin/matchmaking/activities/"+id+"/"+action, input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		if a.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		switch action {
		case "publish":
			if a.Status != "draft" || !tx.Now().Before(a.RegistrationClosesAt) {
				return "", command.ErrConflict
			}
			if err := s.cfg.CheckText(ctx, tx.DB(), a.Title+"\n"+a.Description); err != nil {
				return "", err
			}
			if err := tx.DB().Model(&a).Updates(map[string]any{"status": "published", "published_at": tx.Now()}).Error; err != nil {
				return "", err
			}
		case "cancel":
			if a.Status == "cancelled" {
				return "", command.ErrConflict
			}
			if err := tx.DB().Model(&a).Update("status", "cancelled").Error; err != nil {
				return "", err
			}
			pairs := []entity.MatchmakingPair{}
			if err := tx.DB().Where("activity_id=? AND status<>'ended'", id).Find(&pairs).Error; err != nil {
				return "", err
			}
			for _, p := range pairs {
				if err := endPair(tx, p, input.Reason); err != nil {
					return "", err
				}
			}
		case "results/publish":
			if a.Status != "published" || tx.Now().Before(a.ResultsAt) || !tx.Now().Before(a.EndsAt) {
				return "", command.ErrConflict
			}
			if err := s.publishResults(ctx, tx, a); err != nil {
				return "", err
			}
		}
		if err := bump(tx.DB(), id); err != nil {
			return "", err
		}
		return id, audit(tx.DB(), id, tx.Actor().ID, id, op, input.Reason, tx.Now())
	}, nil)
}

// SaveTemplates 原子替换活动任务库，任何已发布历史关系都会冻结题库。
func (s *Service) SaveTemplates(ctx context.Context, token, key, rid, id string, input TemplatesInput) (Receipt, error) {
	if !validID(id) || input.ExpectedVersion < 1 || input.Items == nil || len(input.Items) > 30 {
		return Receipt{}, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, id, "templates", "PUT", "/api/v1/admin/matchmaking/activities/"+id+"/templates", input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		if a.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		// 配对会随隐私清理物理删除，永久冻结必须由不含个人资料的活动标记承担。
		if a.FirstTaskID != nil || a.TasksPublishedAt != nil || a.Status == "cancelled" || !tx.Now().Before(a.EndsAt) {
			return "", command.ErrConflict
		}
		// 允许先保存不足天数的草稿，发布结果时才要求题库完整。
		seen := map[int]bool{}
		for _, v := range input.Items {
			if !text(v.Body, 1000) || !slices.Contains([]string{"online", "offline"}, v.Kind) || v.FixedDay < 0 || v.FixedDay > a.Days || v.FixedDay > 0 && seen[v.FixedDay] {
				return "", command.ErrInvalid
			}
			seen[v.FixedDay] = true
			if err := s.cfg.CheckText(ctx, tx.DB(), v.Body); err != nil {
				return "", err
			}
		}
		if err := tx.DB().Where("activity_id=?", id).Delete(&entity.MatchmakingTemplate{}).Error; err != nil {
			return "", err
		}
		for _, v := range input.Items {
			if err := tx.DB().Create(&entity.MatchmakingTemplate{ID: uuid.NewString(), ActivityID: id, Kind: v.Kind, Body: v.Body, FixedDay: v.FixedDay}).Error; err != nil {
				return "", err
			}
		}
		if err := bump(tx.DB(), id); err != nil {
			return "", err
		}
		return id, audit(tx.DB(), id, tx.Actor().ID, id, "templates", "", tx.Now())
	}, nil)
}

// Enroll 在报名窗口提交完整资料，修改重新审核，不允许修改已进入配对的资料。
func (s *Service) Enroll(ctx context.Context, token, key, rid, id string, input EnrollmentInput) (Receipt, error) {
	if !validID(id) || input.ExpectedVersion < 0 || input.ProfileVersion < 1 || input.AccountVersion < 1 || !input.Consent {
		return Receipt{}, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, id, "enroll", "PUT", "/api/v1/matchmaking/activities/"+id+"/enrollment", input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		if a.Status != "published" || tx.Now().Before(a.RegistrationOpensAt) || !tx.Now().Before(a.RegistrationClosesAt) {
			return "", command.ErrConflict
		}
		personal, err := s.accountProfile(tx.DB(), tx.Actor().ID)
		if err != nil {
			return "", err
		}
		if personal.Version != input.ProfileVersion || personal.AccountVersion != input.AccountVersion {
			return "", command.ErrVersionConflict
		}
		profile := *personal.Profile
		profile.Consent = true
		if err = s.validateEnrollmentProfile(ctx, profile); err != nil {
			return "", err
		}
		if err = s.cfg.CheckText(ctx, tx.DB(), profile.Name+"\n"+profile.Introduction); err != nil {
			return "", err
		}
		var row entity.MatchmakingEnrollment
		err = tx.DB().Where("activity_id=? AND account_id=?", id, tx.Actor().ID).Take(&row).Error
		fresh := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !fresh {
			return "", err
		}
		if row.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if fresh {
			var total int64
			if err = tx.DB().Model(&entity.MatchmakingEnrollment{}).Where("activity_id=?", id).Count(&total).Error; err != nil {
				return "", err
			}
			if total >= 2000 {
				return "", command.ErrConflict
			}
			row = entity.MatchmakingEnrollment{ID: uuid.NewString(), ActivityID: id, AccountID: tx.Actor().ID, CreatedAt: tx.Now()}
		} else {
			free, e := unused(tx.DB(), row.ID)
			if e != nil {
				return "", e
			}
			if !free {
				return "", command.ErrConflict
			}
		}
		row.Version++
		row.ProfileVersion++
		row.Status, row.Reason = "pending", ""
		row.ProfileCipher, err = s.encrypt(ctx, row, profile)
		if err != nil {
			return "", err
		}
		if fresh {
			err = tx.DB().Create(&row).Error
		} else {
			err = tx.DB().Model(&row).Select("*").Updates(&row).Error
		}
		return row.ID, err
	}, nil)
}

// Review 仅审核当前报名资料，失败原因与通知同事务保存。
func (s *Service) Review(ctx context.Context, token, key, rid, id, enrollmentID string, input ReviewInput) (Receipt, error) {
	if !validID(id) || !validID(enrollmentID) || input.ExpectedVersion < 1 || len([]rune(input.Reason)) > 200 || !slices.Contains([]string{"approved", "rejected"}, input.Decision) || input.Decision == "rejected" && !text(input.Reason, 200) {
		return Receipt{}, command.ErrInvalid
	}
	path := "/api/v1/admin/matchmaking/activities/" + id + "/enrollments/" + enrollmentID + "/review"
	return s.write(ctx, token, key, rid, id, "review", "POST", path, input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		row, err := enrollment(tx.DB(), id, enrollmentID)
		if err != nil {
			return "", err
		}
		if row.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if row.Status != "pending" || a.Status != "published" || !tx.Now().Before(a.EndsAt) {
			return "", command.ErrConflict
		}
		if input.Decision == "approved" {
			ok, e := eligible(tx.DB(), row.AccountID, tx.Now())
			if e != nil {
				return "", e
			}
			if !ok {
				return "", command.ErrConflict
			}
			profile, e := s.decrypt(ctx, row)
			if e != nil {
				return "", e
			}
			if e = s.cfg.CheckText(ctx, tx.DB(), profile.Name+"\n"+profile.Introduction); e != nil {
				return "", e
			}
		}
		row.Status, row.Reason = input.Decision, input.Reason
		if row.Status == "approved" {
			row.Reason = ""
		}
		row.Version++
		if err = tx.DB().Model(&row).Select("status", "reason", "version").Updates(&row).Error; err != nil {
			return "", err
		}
		if err = notify(tx.DB(), row.AccountID, "matchmaking_review", row.ID, tx.Now()); err != nil {
			return "", err
		}
		return row.ID, audit(tx.DB(), id, tx.Actor().ID, row.ID, "review_"+row.Status, row.Reason, tx.Now())
	}, nil)
}

// Withdraw 允许受限账号退出本人活动，解除关系后双方立即失去互访资格。
func (s *Service) Withdraw(ctx context.Context, token, key, rid, id string, input VersionInput) (Receipt, error) {
	if !validID(id) || input.ExpectedVersion < 1 || len([]rune(input.Reason)) > 200 {
		return Receipt{}, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, id, "withdraw", "POST", "/api/v1/matchmaking/activities/"+id+"/withdraw", input, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		var row entity.MatchmakingEnrollment
		if err := tx.DB().Where("activity_id=? AND account_id=?", id, tx.Actor().ID).Take(&row).Error; err != nil {
			return "", err
		}
		if row.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if row.Status == "withdrawn" {
			return "", command.ErrConflict
		}
		members := []entity.MatchmakingMember{}
		if err := tx.DB().Where("enrollment_id=? AND active", row.ID).Find(&members).Error; err != nil {
			return "", err
		}
		for _, m := range members {
			var pair entity.MatchmakingPair
			if err := tx.DB().Where("id=?", m.PairID).Take(&pair).Error; err != nil {
				return "", err
			}
			if err := endPair(tx, pair, "成员退出活动"); err != nil {
				return "", err
			}
		}
		if err := tx.DB().Model(&row).Updates(map[string]any{"status": "withdrawn", "reason": "本人退出活动", "version": row.Version + 1}).Error; err != nil {
			return "", err
		}
		// 退出会改变运营已读取的配对集合，旧版本的批量发布须重新核对。
		if len(members) > 0 {
			if err := bump(tx.DB(), id); err != nil {
				return "", err
			}
		}
		return row.ID, audit(tx.DB(), id, tx.Actor().ID, row.ID, "withdraw", "本人退出活动", tx.Now())
	}, nil)
}

// OpenTask 按完整数据库时间和当前成员资格记录本人首次查看，不写回另一人的记录。
func (s *Service) OpenTask(ctx context.Context, token, key, rid, id, taskID string) (Receipt, error) {
	if !validID(id) || !validID(taskID) {
		return Receipt{}, command.ErrInvalid
	}
	var task entity.MatchmakingTask
	authorize := func(tx *command.Tx, a entity.MatchmakingActivity) error {
		if err := tx.DB().Where("id=?", taskID).Take(&task).Error; err != nil {
			return err
		}
		var pair entity.MatchmakingPair
		if err := tx.DB().Where("id=? AND activity_id=? AND status='published'", task.PairID, id).Take(&pair).Error; err != nil {
			return err
		}
		members, err := pairMembers(tx.DB(), pair.ID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(members, func(m entity.MatchmakingMember) bool { return m.AccountID == tx.Actor().ID }) {
			return command.ErrNotFound
		}
		ok, err := pairEligible(tx.DB(), members, tx.Now())
		if err != nil {
			return err
		}
		if !ok || a.Status != "published" {
			return command.ErrForbidden
		}
		if tx.Now().Before(task.UnlocksAt) || tx.Now().Before(a.StartsAt) || !tx.Now().Before(a.EndsAt) {
			return command.ErrConflict
		}
		return nil
	}
	path := "/api/v1/matchmaking/activities/" + id + "/tasks/" + taskID + "/open"
	return s.write(ctx, token, key, rid, id, "task_open", "POST", path, struct{}{}, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		err := tx.DB().Clauses(clause.OnConflict{DoNothing: true}).Create(&entity.MatchmakingOpen{TaskID: taskID, AccountID: tx.Actor().ID, OpenedAt: tx.Now()}).Error
		return taskID, err
	}, authorize)
}
