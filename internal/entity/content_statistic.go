package entity

// ContentStatistic 是无读者身份的累计详情打开次数。
type ContentStatistic struct {
	TargetKind string `gorm:"column:target_kind;primaryKey;comment:内容固定种类" json:"-"`
	TargetID   string `gorm:"column:target_id;type:uuid;primaryKey;autoIncrement:false;comment:内容引用" json:"-"`
	ViewCount  int64  `gorm:"column:view_count;comment:累计有效详情打开次数" json:"-"`
}

// TableName 返回显式业务表名。
func (ContentStatistic) TableName() string { return "public.content_statistics" }
