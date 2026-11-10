package entity

import (
	"time"
)

// MatchmakingOpen 映射成员独立任务查看记录，授权由领域服务复核。
type MatchmakingOpen struct {
	TaskID    string    `gorm:"column:task_id;comment:所属任务;primaryKey;autoIncrement:false" json:"-"`
	AccountID string    `gorm:"column:account_id;comment:查看账号;primaryKey;autoIncrement:false" json:"-"`
	OpenedAt  time.Time `gorm:"column:opened_at;comment:本人首次查看时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingOpen) TableName() string { return "public.matchmaking_opens" }
