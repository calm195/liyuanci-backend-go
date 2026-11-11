package entity

import "time"

// MatchmakingTraitSelection 保存当前选用关系，账号删除时级联清除。
type MatchmakingTraitSelection struct {
	AccountID string    `gorm:"column:account_id;type:uuid;primaryKey;comment:选用者账号"`
	Purpose   string    `gorm:"column:purpose;primaryKey;default:ideal;comment:选用用途，个人或理想型"`
	TraitID   string    `gorm:"column:trait_id;type:uuid;primaryKey;comment:所选特质"`
	CreatedAt time.Time `gorm:"column:created_at;not null;comment:本次选用时间"`
}

// TableName 返回当前选用关系表。
func (MatchmakingTraitSelection) TableName() string { return "public.matchmaking_trait_selections" }
