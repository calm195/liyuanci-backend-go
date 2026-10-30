package entity

import (
	"encoding/json"
	"time"
)

// ResourceMaterial 映射外部下载资料元信息。
type ResourceMaterial struct {
	ID             string          `gorm:"column:id;type:uuid;not null;comment:稳定资料标识;primaryKey;autoIncrement:false" json:"-"`
	CategoryID     string          `gorm:"column:category_id;type:uuid;not null;comment:启用分类引用" json:"-"`
	Title          string          `gorm:"column:title;type:text;not null;comment:规范标题" json:"-"`
	Description    string          `gorm:"column:description;type:text;not null;comment:规范简介" json:"-"`
	DownloadCipher json.RawMessage `gorm:"column:download_cipher;type:jsonb;not null;comment:用途及内容版本绑定的链接和提取码密文" json:"-"`
	Status         string          `gorm:"column:status;type:text;not null;comment:资料发布状态" json:"-"`
	Version        int64           `gorm:"column:version;type:bigint;not null;comment:资料并发版本" json:"-"`
	ContentVersion int64           `gorm:"column:content_version;type:bigint;not null;comment:正文版本" json:"-"`
	ContentBlocked bool            `gorm:"column:content_blocked;type:boolean;default:false;not null;comment:受控内容屏障" json:"-"`
	PrivacyBlocked bool            `gorm:"column:privacy_blocked;type:boolean;default:false;not null;comment:受控隐私屏障" json:"-"`
	PublishedAt    *time.Time      `gorm:"column:published_at;type:timestamptz;comment:最近上架时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	CreatedAt      time.Time       `gorm:"column:created_at;type:timestamptz;not null;comment:不可变创建时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
	UpdatedAt      time.Time       `gorm:"column:updated_at;type:timestamptz;not null;comment:修改时间;autoCreateTime:false;autoUpdateTime:false" json:"-"`
}

// TableName 返回显式业务表名。
func (ResourceMaterial) TableName() string { return "public.resource_materials" }
