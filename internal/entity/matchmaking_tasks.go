package entity

import (
	"time"
)

// MatchmakingTask 映射配对任务不可变快照，授权由领域服务复核。
type MatchmakingTask struct {
	ID        string    `gorm:"column:id;comment:任务随机标识;primaryKey;autoIncrement:false" json:"-"`
	PairID    string    `gorm:"column:pair_id;comment:所属配对" json:"-"`
	DayIndex  int       `gorm:"column:day_index;comment:从一开始的任务天数" json:"-"`
	Kind      string    `gorm:"column:kind;comment:任务类型快照" json:"-"`
	Body      string    `gorm:"column:body;comment:任务正文快照" json:"-"`
	UnlocksAt time.Time `gorm:"column:unlocks_at;comment:完整解锁时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingTask) TableName() string { return "public.matchmaking_tasks" }
