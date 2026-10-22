package entity

// ReportType 保存运营显式发布的举报类型。
type ReportType struct {
	Code      string `gorm:"column:code;type:text;primaryKey;comment:稳定举报类型码" json:"code"`
	Label     string `gorm:"column:label;type:text;not null;comment:面向举报人的类型名称" json:"label"`
	Published bool   `gorm:"column:published;not null;default:false;comment:是否发布供举报使用" json:"-"`
}

// TableName 返回举报类型表。
func (ReportType) TableName() string { return "public.report_types" }
