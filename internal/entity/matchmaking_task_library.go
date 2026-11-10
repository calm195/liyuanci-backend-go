package entity

// MatchmakingLibraryTask 映射独立任务库，授权由领域服务复核。
type MatchmakingLibraryTask struct {
	ID      string `gorm:"column:id;primaryKey;autoIncrement:false;comment:任务随机标识" json:"-"`
	Version int64  `gorm:"column:version;comment:乐观版本" json:"-"`
	Kind    string `gorm:"column:kind;comment:线上或线下类型" json:"-"`
	Body    string `gorm:"column:body;comment:任务正文" json:"-"`
	MinDay  int    `gorm:"column:min_day;comment:最早可分配日" json:"-"`
	Enabled bool   `gorm:"column:enabled;comment:是否启用" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingLibraryTask) TableName() string { return "public.matchmaking_task_library" }
