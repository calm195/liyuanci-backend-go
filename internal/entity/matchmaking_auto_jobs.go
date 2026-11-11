package entity

import (
	"encoding/json"
	"time"
)

// MatchmakingAutoJob 保存全员分配的持久任务和加密预览，不直接占用报名者。
type MatchmakingAutoJob struct {
	ID               string          `gorm:"column:id;type:uuid;primaryKey;comment:预览任务标识"`
	ActivityID       string          `gorm:"column:activity_id;type:uuid;comment:所属活动"`
	Version          int64           `gorm:"column:version;comment:任务乐观版本"`
	ActivityVersion  int64           `gorm:"column:activity_version;comment:生成时活动版本"`
	Status           string          `gorm:"column:status;comment:任务阶段"`
	Seed             int64           `gorm:"column:seed;comment:稳定随机种子"`
	Algorithm        string          `gorm:"column:algorithm;comment:求解及计分规则版本"`
	InputHash        string          `gorm:"column:input_hash;comment:完整输入指纹"`
	ResultCipher     json.RawMessage `gorm:"column:result_cipher;type:jsonb;comment:方案密文"`
	ParticipantCount int             `gorm:"column:participant_count;comment:报名总人数"`
	EligibleCount    int             `gorm:"column:eligible_count;comment:本轮合格人数"`
	PairedCount      int             `gorm:"column:paired_count;comment:配对组数"`
	UnmatchedCount   int             `gorm:"column:unmatched_count;comment:合格落单人数"`
	ExcludedCount    int             `gorm:"column:excluded_count;comment:排除人数"`
	TotalScore       float64         `gorm:"column:total_score;comment:总分"`
	Error            string          `gorm:"column:error;comment:安全错误说明"`
	CreatedAt        time.Time       `gorm:"column:created_at;comment:创建时间"`
	ExpiresAt        time.Time       `gorm:"column:expires_at;comment:确认过期时间"`
	RequestedBy      *string         `gorm:"column:requested_by;type:uuid;comment:创建运营"`
}

// TableName 返回预览持久任务表。
func (MatchmakingAutoJob) TableName() string { return "public.matchmaking_auto_jobs" }
