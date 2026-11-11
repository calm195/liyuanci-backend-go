package entity

import (
	"encoding/json"
	"time"
)

// MatchmakingTrait 保存共享特质及独立审核案件，待审核词条仅本人和审核运营可见。
type MatchmakingTrait struct {
	ID         string          `gorm:"column:id;type:uuid;primaryKey;comment:词条标识"`
	Name       string          `gorm:"column:name;not null;comment:规范化的特质词条"`
	Status     string          `gorm:"column:status;not null;comment:词条审核状态"`
	Version    int64           `gorm:"column:version;not null;comment:词条乐观版本"`
	Evidence   json.RawMessage `gorm:"column:evidence;type:jsonb;not null;comment:创建时词库扫描依据"`
	Reason     string          `gorm:"column:reason;not null;comment:人工审核说明"`
	CreatedAt  time.Time       `gorm:"column:created_at;not null;comment:词条创建时间"`
	ReviewedAt *time.Time      `gorm:"column:reviewed_at;comment:人工审核时间"`
	ReviewedBy *string         `gorm:"column:reviewed_by;type:uuid;comment:审核管理员"`
}

// TableName 返回理想型特质表。
func (MatchmakingTrait) TableName() string { return "public.matchmaking_traits" }
