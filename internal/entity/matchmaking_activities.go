package entity

import (
	"encoding/json"
	"time"
)

// MatchmakingActivity 映射包分配活动，授权由领域服务复核。
type MatchmakingActivity struct {
	ID                   string          `gorm:"column:id;comment:活动随机标识;primaryKey;autoIncrement:false" json:"-"`
	Version              int64           `gorm:"column:version;comment:乐观版本" json:"-"`
	Title                string          `gorm:"column:title;comment:活动名称" json:"-"`
	Description          string          `gorm:"column:description;comment:经运营审核的活动说明" json:"-"`
	FirstTaskID          *string         `gorm:"column:first_task_id;comment:任务库固定首日任务" json:"-"`
	TasksPublishedAt     *time.Time      `gorm:"column:tasks_published_at;comment:首次任务发布时刻，关系清理不解冻题库;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	PublishedAt          *time.Time      `gorm:"column:published_at;comment:首次发布时刻;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	Status               string          `gorm:"column:status;comment:活动发布状态" json:"-"`
	Campuses             json.RawMessage `gorm:"column:campuses;comment:参与校区代码;type:jsonb" json:"-"`
	RegistrationOpensAt  time.Time       `gorm:"column:registration_opens_at;comment:报名开始;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	RegistrationClosesAt time.Time       `gorm:"column:registration_closes_at;comment:报名截止;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	ResultsAt            time.Time       `gorm:"column:results_at;comment:最早结果发布时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	StartsAt             time.Time       `gorm:"column:starts_at;comment:每日任务起始时刻;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	EndsAt               time.Time       `gorm:"column:ends_at;comment:活动结束;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	Days                 int             `gorm:"column:days;comment:任务天数" json:"-"`
	CreatedAt            time.Time       `gorm:"column:created_at;comment:创建时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingActivity) TableName() string { return "public.matchmaking_activities" }
