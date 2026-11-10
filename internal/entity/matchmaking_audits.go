package entity

import (
	"time"
)

// MatchmakingAudit 映射活动运营操作审计，授权由领域服务复核。
type MatchmakingAudit struct {
	ID         string    `gorm:"column:id;comment:审计随机标识;primaryKey;autoIncrement:false" json:"-"`
	ActivityID string    `gorm:"column:activity_id;comment:所属活动" json:"-"`
	ActorID    string    `gorm:"column:actor_id;comment:操作主体技术引用" json:"-"`
	SubjectRef string    `gorm:"column:subject_ref;comment:操作对象技术引用，注销时清理" json:"-"`
	Action     string    `gorm:"column:action;comment:操作代码" json:"-"`
	Reason     string    `gorm:"column:reason;comment:必要操作原因" json:"-"`
	At         time.Time `gorm:"column:at;comment:操作时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingAudit) TableName() string { return "public.matchmaking_audits" }
