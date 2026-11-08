package entity

import (
	"encoding/json"
	"time"
)

// DisplayDictionaryAction 保存展示字典修改审计。
type DisplayDictionaryAction struct {
	Version int64           `gorm:"column:version;primaryKey;autoIncrement:false;comment:对应全局发布版本" json:"-"`
	ActorID string          `gorm:"column:actor_id;type:uuid;not null;comment:执行修改的管理员" json:"-"`
	Before  json.RawMessage `gorm:"column:before;type:jsonb;not null;comment:修改前字典项" json:"-"`
	After   json.RawMessage `gorm:"column:after;type:jsonb;not null;comment:修改后字典项" json:"-"`
	At      time.Time       `gorm:"column:at;type:timestamptz;not null;comment:事务修改时间" json:"-"`
}

// TableName 返回展示字典表名。
func (DisplayDictionaryAction) TableName() string { return "public.display_dictionary_actions" }
