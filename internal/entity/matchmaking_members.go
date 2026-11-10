package entity

// MatchmakingMember 映射活动配对成员及唯一占用，授权由领域服务复核。
type MatchmakingMember struct {
	PairID         string `gorm:"column:pair_id;comment:所属配对;primaryKey;autoIncrement:false" json:"-"`
	EnrollmentID   string `gorm:"column:enrollment_id;comment:报名引用;primaryKey;autoIncrement:false" json:"-"`
	ActivityID     string `gorm:"column:activity_id;comment:所属活动" json:"-"`
	AccountID      string `gorm:"column:account_id;comment:成员账号" json:"-"`
	ProfileVersion int64  `gorm:"column:profile_version;comment:配对时冻结的资料版本" json:"-"`
	Active         bool   `gorm:"column:active;comment:是否占用本活动配对名额" json:"-"`
}

// TableName 返回固定业务表。
func (MatchmakingMember) TableName() string { return "public.matchmaking_members" }
