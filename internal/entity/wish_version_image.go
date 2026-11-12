package entity

// WishVersionImage 保存精确内容版本的图片顺序。
type WishVersionImage struct {
	WishID         string `gorm:"column:wish_id;type:uuid;primaryKey;autoIncrement:false;comment:所属心愿" json:"-"`
	ContentVersion int64  `gorm:"column:content_version;primaryKey;autoIncrement:false;comment:所属精确正文版本" json:"-"`
	ImageID        string `gorm:"column:image_id;type:uuid;primaryKey;autoIncrement:false;comment:已审核的本人图片" json:"-"`
	Position       int16  `gorm:"column:position;type:smallint;not null;comment:展示顺序，零至二" json:"-"`
}

// TableName 返回图片版本关联表。
func (WishVersionImage) TableName() string { return "public.wish_version_images" }
