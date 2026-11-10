package entity

// MatchmakingTemplate 映射每日任务模板，授权由领域服务复核。
type MatchmakingTemplate struct {
	ID         string `gorm:"column:id;comment:模板随机标识;primaryKey;autoIncrement:false" json:"-"`
	ActivityID string `gorm:"column:activity_id;comment:所属活动" json:"-"`
	Kind       string `gorm:"column:kind;comment:线上或线下类型" json:"-"`
	Body       string `gorm:"column:body;comment:任务正文" json:"-"`
	MinDay     int    `gorm:"column:min_day;comment:最早可分配日" json:"-"`
	FixedDay   int    `gorm:"column:fixed_day;comment:固定第几天，零表示随机候选" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingTemplate) TableName() string { return "public.matchmaking_templates" }
