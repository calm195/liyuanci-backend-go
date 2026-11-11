package entity

import (
	"encoding/json"
	"time"
)

// MatchmakingPair 映射独立活动配对，授权由领域服务复核。
type MatchmakingPair struct {
	ID           string          `gorm:"column:id;comment:配对随机标识;primaryKey;autoIncrement:false" json:"-"`
	ActivityID   string          `gorm:"column:activity_id;comment:所属活动" json:"-"`
	Version      int64           `gorm:"column:version;comment:配对乐观版本" json:"-"`
	Status       string          `gorm:"column:status;comment:配对状态" json:"-"`
	Score        float64         `gorm:"column:score;comment:冻结推荐分数" json:"-"`
	ScoreDetails json.RawMessage `gorm:"column:score_details;comment:分项得分;type:jsonb" json:"-"`
	Algorithm    string          `gorm:"column:algorithm;comment:评分规则版本" json:"-"`
	PublishedAt  *time.Time      `gorm:"column:published_at;comment:发布时刻;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	EndedAt      *time.Time      `gorm:"column:ended_at;comment:解除时刻;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	Reason       string          `gorm:"column:reason;comment:解除原因" json:"-"`
	CreatedAt    time.Time       `gorm:"column:created_at;comment:创建时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingPair) TableName() string { return "public.matchmaking_pairs" }
