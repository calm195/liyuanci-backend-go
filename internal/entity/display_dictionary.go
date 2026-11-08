package entity

// DisplayDictionary 保存展示字典全局发布版本。
type DisplayDictionary struct {
	ID      int64 `gorm:"column:id;primaryKey;autoIncrement:false;comment:唯一字典版本行" json:"-"`
	Version int64 `gorm:"column:version;not null;comment:全局乐观发布版本" json:"-"`
}

// TableName 返回展示字典表名。
func (DisplayDictionary) TableName() string { return "public.display_dictionaries" }
