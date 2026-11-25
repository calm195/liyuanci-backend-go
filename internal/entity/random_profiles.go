package entity

import (
	"encoding/json"
	"time"
)

// RandomProfileImage 保存通过检查的不可变头像字节。
type RandomProfileImage struct {
	ID  string `gorm:"column:id;type:uuid;primaryKey;comment:不可变图片随机标识" json:"-"`
	PNG []byte `gorm:"column:png;type:bytea;not null;comment:已检查并规范化的PNG展示图" json:"-"`
}

// TableName 返回随机资料图片表名。
func (RandomProfileImage) TableName() string { return "public.random_profile_images" }

// RandomProfileAvatar 保存可停用的随机头像及当前图片引用。
type RandomProfileAvatar struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey;comment:头像素材随机标识" json:"-"`
	Name      string    `gorm:"column:name;type:text;not null;comment:后台素材名称" json:"-"`
	ImageID   string    `gorm:"column:image_id;type:uuid;not null;comment:当前通过检查的不可变展示图片" json:"-"`
	Enabled   bool      `gorm:"column:enabled;not null;comment:是否参与新抽取" json:"-"`
	Version   int64     `gorm:"column:version;not null;comment:素材维护乐观版本" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:timestamptz;not null;autoUpdateTime:false;comment:素材最近更新时间" json:"-"`
}

// TableName 返回随机资料头像表名。
func (RandomProfileAvatar) TableName() string { return "public.random_profile_avatars" }

// RandomProfileWord 保存一种组成随机昵称的词条。
type RandomProfileWord struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey;comment:词条随机标识" json:"-"`
	Kind      string    `gorm:"column:kind;type:text;not null;comment:词条种类，形容词或名词" json:"-"`
	Text      string    `gorm:"column:text;type:text;not null;comment:用于拼接的词条文本" json:"-"`
	Enabled   bool      `gorm:"column:enabled;not null;comment:是否参与新抽取" json:"-"`
	Version   int64     `gorm:"column:version;not null;comment:词条维护乐观版本" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:timestamptz;not null;autoUpdateTime:false;comment:词条最近更新时间" json:"-"`
}

// TableName 返回随机资料词条表名。
func (RandomProfileWord) TableName() string { return "public.random_profile_words" }

// RandomProfileAction 保存维护审计及可幂等重放的结果快照。
type RandomProfileAction struct {
	ID        string          `gorm:"column:id;type:uuid;primaryKey;comment:操作审计及命令结果标识" json:"-"`
	ActorID   string          `gorm:"column:actor_id;type:uuid;not null;comment:执行操作的管理员" json:"-"`
	TargetID  string          `gorm:"column:target_id;type:uuid;not null;comment:被维护的头像素材或词条" json:"-"`
	Operation string          `gorm:"column:operation;type:text;not null;comment:后台维护动作" json:"-"`
	Before    json.RawMessage `gorm:"column:before;type:jsonb;not null;comment:修改前投影，新建记录为JSON空值" json:"-"`
	After     json.RawMessage `gorm:"column:after;type:jsonb;not null;comment:修改后投影" json:"-"`
	At        time.Time       `gorm:"column:at;type:timestamptz;not null;comment:维护提交时间" json:"-"`
}

// TableName 返回随机资料维护审计表名。
func (RandomProfileAction) TableName() string { return "public.random_profile_actions" }
