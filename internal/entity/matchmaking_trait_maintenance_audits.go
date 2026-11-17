package entity

import "time"

// MatchmakingTraitMaintenanceAudit 独立记录后台词条维护，不替代词库审核证据。
type MatchmakingTraitMaintenanceAudit struct {
	ID          string    `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:审计随机标识" json:"-"`
	ActorID     string    `gorm:"column:actor_id;type:uuid;not null;comment:后台操作主体" json:"-"`
	TraitID     string    `gorm:"column:trait_id;type:uuid;not null;comment:共享词条标识" json:"-"`
	Action      string    `gorm:"column:action;not null;comment:维护动作" json:"-"`
	OldCategory *string   `gorm:"column:old_category;comment:维护前分类，新建为空" json:"-"`
	NewCategory string    `gorm:"column:new_category;not null;comment:维护后分类" json:"-"`
	OldEnabled  *bool     `gorm:"column:old_enabled;comment:维护前启用状态，新建为空" json:"-"`
	NewEnabled  bool      `gorm:"column:new_enabled;not null;comment:维护后启用状态" json:"-"`
	Reason      string    `gorm:"column:reason;not null;comment:本次维护理由，独立于审核理由" json:"-"`
	Version     int64     `gorm:"column:version;not null;comment:操作完成后的词条版本" json:"-"`
	At          time.Time `gorm:"column:at;not null;autoCreateTime:false;autoUpdateTime:false;comment:维护提交时间" json:"-"`
}

func (MatchmakingTraitMaintenanceAudit) TableName() string {
	return "public.matchmaking_trait_maintenance_audits"
}
