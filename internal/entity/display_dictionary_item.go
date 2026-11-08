package entity

// DisplayDictionaryItem 保存固定业务代码的可维护展示信息。
type DisplayDictionaryItem struct {
	Type      string `gorm:"column:type;primaryKey;type:text;comment:字典类型" json:"-"`
	Code      string `gorm:"column:code;primaryKey;type:text;comment:稳定业务代码" json:"-"`
	Label     string `gorm:"column:label;type:text;not null;comment:中文展示名称" json:"-"`
	SortOrder int    `gorm:"column:sort_order;not null;comment:展示排序" json:"-"`
	Enabled   bool   `gorm:"column:enabled;not null;comment:选项是否启用，不改变业务权限" json:"-"`
	Version   int64  `gorm:"column:version;not null;comment:最近修改的全局版本" json:"-"`
}

// TableName 返回展示字典表名。
func (DisplayDictionaryItem) TableName() string { return "public.display_dictionary_items" }
