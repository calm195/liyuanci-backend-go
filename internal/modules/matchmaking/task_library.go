package matchmaking

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// LibraryTaskInput 保存一个任务，版本零用于新建。
type LibraryTaskInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Kind            string `json:"kind"`
	Body            string `json:"body"`
	MinDay          int    `json:"min_day"`
	Enabled         *bool  `json:"enabled"`
}

// LibraryTask 是仅供管理端使用的任务库条目。
type LibraryTask struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Kind    string `json:"kind"`
	Body    string `json:"body"`
	MinDay  int    `json:"min_day"`
	Enabled bool   `json:"enabled"`
}

// LibraryPage 是独立于活动的任务列表。
type LibraryPage struct {
	Items []LibraryTask `json:"items"`
	Total int64         `json:"total"`
}

// TaskPlanInput 在首次发布结果之前选择活动的固定首日任务。
type TaskPlanInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	FirstTaskID     string `json:"first_task_id"`
}

// TaskPlan 包含首日任务和最早无法排满的天数；零表示能排满。
type TaskPlan struct {
	FirstTaskID   string `json:"first_task_id"`
	FirstTaskBody string `json:"first_task_body"`
	MissingDay    int    `json:"missing_day"`
}

// ReadLibrary 在运营权限下分页读取任务库，包括停用条目。
func (s *Service) ReadLibrary(ctx context.Context, token, rid string, page, limit int) (out LibraryPage, err error) {
	defer s.done("library_read", rid, time.Now(), &err)
	if page < 1 || page > 10000 || limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, "", true, func(db *gorm.DB, _ reader) error {
		q := db.Model(&entity.MatchmakingLibraryTask{})
		if e := q.Count(&out.Total).Error; e != nil {
			return e
		}
		var rows []entity.MatchmakingLibraryTask
		if e := q.Order("min_day,id").Limit(limit).Offset((page - 1) * limit).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = make([]LibraryTask, 0, len(rows))
		for _, row := range rows {
			out.Items = append(out.Items, LibraryTask{
				ID: row.ID, Version: row.Version, Kind: row.Kind, Body: row.Body, MinDay: row.MinDay, Enabled: row.Enabled,
			})
		}
		return nil
	})
	return out, err
}

// SaveLibraryTask 使用固定幂等键和乐观版本创建或更新任务。
func (s *Service) SaveLibraryTask(ctx context.Context, token, key, rid, id string, input LibraryTaskInput) (out Receipt, err error) {
	defer s.done("library_write", rid, time.Now(), &err)
	if !text(input.Body, 1000) || !slices.Contains([]string{"online", "offline"}, input.Kind) ||
		input.MinDay < 0 || input.MinDay > 30 || input.Enabled == nil ||
		id == "" && input.ExpectedVersion != 0 || id != "" && (!validID(id) || input.ExpectedVersion < 1) {
		return out, command.ErrInvalid
	}
	method, path, op := "POST", "/api/v1/admin/matchmaking/task-library", "library_create"
	if id != "" {
		method, path, op = "PUT", path+"/"+id, "library_save"
	}
	return s.write(ctx, token, key, rid, "", op, method, path, input, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		if e := s.cfg.CheckText(ctx, tx.DB(), input.Body); e != nil {
			return "", e
		}
		if id == "" {
			next := entity.MatchmakingLibraryTask{
				ID: uuid.NewString(), Version: 1, Kind: input.Kind, Body: input.Body, MinDay: input.MinDay, Enabled: *input.Enabled,
			}
			return next.ID, tx.DB().Create(&next).Error
		}
		result := tx.DB().Model(&entity.MatchmakingLibraryTask{}).Where("id=? AND version=?", id, input.ExpectedVersion).
			Updates(map[string]any{"kind": input.Kind, "body": input.Body, "min_day": input.MinDay, "enabled": *input.Enabled, "version": input.ExpectedVersion + 1})
		if result.Error != nil {
			return "", result.Error
		}
		if result.RowsAffected != 1 {
			return "", command.ErrVersionConflict
		}
		return id, nil
	}, nil)
}

// SaveTaskPlan 切换到独立任务库，并选择固定首日任务。
func (s *Service) SaveTaskPlan(ctx context.Context, token, key, rid, id string, input TaskPlanInput) (out Receipt, err error) {
	defer s.done("task_plan_write", rid, time.Now(), &err)
	if !validID(id) || !validID(input.FirstTaskID) || input.ExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	path := "/api/v1/admin/matchmaking/activities/" + id + "/task-plan"
	return s.write(ctx, token, key, rid, id, "task_plan", "PUT", path, input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		if a.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if a.TasksPublishedAt != nil || a.Status == "cancelled" || !tx.Now().Before(a.EndsAt) {
			return "", command.ErrConflict
		}
		var count int64
		if e := tx.DB().Model(&entity.MatchmakingLibraryTask{}).
			Where("id=? AND enabled AND min_day<=1", input.FirstTaskID).Count(&count).Error; e != nil {
			return "", e
		}
		if count != 1 {
			return "", command.ErrConflict
		}
		if e := tx.DB().Model(&a).Updates(map[string]any{"first_task_id": input.FirstTaskID, "version": a.Version + 1}).Error; e != nil {
			return "", e
		}
		return id, audit(tx.DB(), id, tx.Actor().ID, id, "task_plan", "", tx.Now())
	}, nil)
}

func libraryTemplates(db *gorm.DB, a entity.MatchmakingActivity) ([]Template, error) {
	var rows []entity.MatchmakingLibraryTask
	if err := db.Where("enabled AND min_day<=?", a.Days).Order("min_day,id").Find(&rows).Error; err != nil {
		return nil, err
	}
	list := make([]Template, 0, len(rows))
	for _, row := range rows {
		v := Template{Kind: row.Kind, Body: row.Body, MinDay: row.MinDay}
		if row.ID == *a.FirstTaskID {
			// 首日任务只在固定位置使用，即使后来更改了最早分配日。
			if row.MinDay > 1 {
				continue
			}
			v.FixedDay = 1
		}
		list = append(list, v)
	}
	return list, nil
}

func readTaskPlan(db *gorm.DB, a entity.MatchmakingActivity) (*TaskPlan, error) {
	load := libraryTemplates
	if a.TasksPublishedAt != nil {
		load = func(db *gorm.DB, a entity.MatchmakingActivity) ([]Template, error) {
			return templates(db, a.ID)
		}
	}
	list, err := load(db, a)
	if err != nil {
		return nil, err
	}
	out := &TaskPlan{FirstTaskID: *a.FirstTaskID, MissingDay: 1}
	for _, item := range list {
		if item.FixedDay == 1 {
			out.FirstTaskBody = item.Body
			out.MissingDay = firstMissingDay(a.Days, list)
			break
		}
	}
	return out, nil
}

func resultTemplates(db *gorm.DB, a entity.MatchmakingActivity) ([]Template, error) {
	if a.FirstTaskID == nil || a.TasksPublishedAt != nil {
		return templates(db, a.ID)
	}
	list, err := libraryTemplates(db, a)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(list, func(v Template) bool { return v.FixedDay == 1 }) || firstMissingDay(a.Days, list) != 0 {
		return nil, command.ErrConflict
	}
	// 同一活动后续补发布继续使用首次发布的任务池；题库编辑只影响尚未首次发布的活动。
	if err = db.Where("activity_id=?", a.ID).Delete(&entity.MatchmakingTemplate{}).Error; err != nil {
		return nil, err
	}
	rows := make([]entity.MatchmakingTemplate, 0, len(list))
	for _, item := range list {
		rows = append(rows, entity.MatchmakingTemplate{
			ID: uuid.NewString(), ActivityID: a.ID, Kind: item.Kind, Body: item.Body, FixedDay: item.FixedDay, MinDay: item.MinDay,
		})
	}
	if err = db.CreateInBatches(&rows, 100).Error; err != nil {
		return nil, err
	}
	return list, nil
}
