package entity

import "encoding/json"

// ModuleGuide 保存使用说明草稿及独立发布快照。
type ModuleGuide struct {
	ID               int64           `gorm:"column:id;primaryKey;autoIncrement:false;comment:唯一配置行标识" json:"-"`
	Version          int64           `gorm:"column:version;not null;comment:草稿并发版本" json:"-"`
	Modules          json.RawMessage `gorm:"column:modules;type:jsonb;not null;comment:模块说明草稿" json:"-"`
	PublishedVersion int64           `gorm:"column:published_version;not null;comment:已发布说明版本，零表示尚未发布" json:"-"`
	PublishedModules json.RawMessage `gorm:"column:published_modules;type:jsonb;not null;comment:已发布模块说明快照" json:"-"`
}

// TableName 返回说明配置表名。
func (ModuleGuide) TableName() string { return "public.module_guides" }

// ModuleGuideAcknowledgement 保存本人已确认的发布版本，账号删除时级联清理。
type ModuleGuideAcknowledgement struct {
	AccountID string `gorm:"column:account_id;type:uuid;primaryKey;autoIncrement:false;comment:确认说明的本人账号" json:"-"`
	Version   int64  `gorm:"column:version;not null;comment:本人最近确认的发布版本" json:"-"`
}

// TableName 返回本人说明确认表名。
func (ModuleGuideAcknowledgement) TableName() string { return "public.module_guide_acknowledgements" }
