package matchmaking

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// PublicActivity 仅包含运营发布的活动说明和时间，不包含报名或结果数据。
type PublicActivity struct {
	ID                   string    `json:"id"`
	Version              int64     `json:"version"`
	Title                string    `json:"title"`
	Description          string    `json:"description"`
	RegistrationOpensAt  time.Time `json:"registration_opens_at"`
	RegistrationClosesAt time.Time `json:"registration_closes_at"`
	ResultsAt            time.Time `json:"results_at"`
	StartsAt             time.Time `json:"starts_at"`
	EndsAt               time.Time `json:"ends_at"`
	Days                 int       `json:"days"`
	Status               string    `json:"status"`
	Phase                string    `json:"phase"`
}

// PublicActivityPage 是游客可见活动的分页结果。
type PublicActivityPage struct {
	Items []PublicActivity `json:"items"`
	Total int64            `json:"total"`
}

var publicActivityColumns = []string{
	"id", "version", "title", "description", "registration_opens_at", "registration_closes_at",
	"results_at", "starts_at", "ends_at", "days", "status",
}

func projectPublicActivity(row entity.MatchmakingActivity, now time.Time) PublicActivity {
	return PublicActivity{
		ID:                   row.ID,
		Version:              row.Version,
		Title:                row.Title,
		Description:          row.Description,
		RegistrationOpensAt:  row.RegistrationOpensAt,
		RegistrationClosesAt: row.RegistrationClosesAt,
		ResultsAt:            row.ResultsAt,
		StartsAt:             row.StartsAt,
		EndsAt:               row.EndsAt,
		Days:                 row.Days,
		Status:               row.Status,
		Phase:                activityPhase(row, now),
	}
}

// readPublicActivities 以同一只读快照计算可见性、分页和活动阶段。
func (s *Service) readPublicActivities(ctx context.Context, run func(*gorm.DB, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var now time.Time
		if err := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return err
		}
		// 已取消活动可能仍有发布时间，游客只可读取当前公开的活动。
		query := db.Model(&entity.MatchmakingActivity{}).Where("status = 'published' AND published_at IS NOT NULL")
		return run(query, now)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

// ListPublicActivities 只分页查询已发布活动元数据，隐藏草稿和取消活动。
func (s *Service) ListPublicActivities(ctx context.Context, rid string, page, limit int) (out PublicActivityPage, err error) {
	defer s.done("public_list", rid, time.Now(), &err)
	if page < 1 || page > 10000 || limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	err = s.readPublicActivities(ctx, func(query *gorm.DB, now time.Time) error {
		if err := query.Count(&out.Total).Error; err != nil {
			return err
		}
		var rows []entity.MatchmakingActivity
		err := query.Select(publicActivityColumns).Order("registration_opens_at DESC, id").
			Limit(limit).Offset((page - 1) * limit).Find(&rows).Error
		if err != nil {
			return err
		}
		out.Items = make([]PublicActivity, 0, len(rows))
		for _, row := range rows {
			out.Items = append(out.Items, projectPublicActivity(row, now))
		}
		return nil
	})
	if err != nil {
		return PublicActivityPage{}, err
	}
	return out, nil
}

// ReadPublicActivity 以独立投影读取活动说明，所有未公开活动统一按不存在处理。
func (s *Service) ReadPublicActivity(ctx context.Context, rid, id string) (out PublicActivity, err error) {
	defer s.done("public_detail", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.readPublicActivities(ctx, func(query *gorm.DB, now time.Time) error {
		var row entity.MatchmakingActivity
		if err := query.Select(publicActivityColumns).Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		out = projectPublicActivity(row, now)
		return nil
	})
	if err != nil {
		return PublicActivity{}, err
	}
	return out, nil
}
