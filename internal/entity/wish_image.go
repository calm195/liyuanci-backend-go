package entity

import (
	"encoding/json"
	"time"
)

// WishImage 保存图片加密对象及上传清理状态。
type WishImage struct {
	ID               string          `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:图片随机标识" json:"-"`
	OwnerID          *string         `gorm:"column:owner_id;type:uuid;comment:上传账号，注销后清空并保留对象清理引用" json:"-"`
	ObjectKey        string          `gorm:"column:object_key;not null;comment:独立心愿图片命名空间对象键" json:"-"`
	ObjectVersion    string          `gorm:"column:object_version;not null;default:'';comment:私有密文对象完整性版本" json:"-"`
	ByteSize         int64           `gorm:"column:byte_size;not null;comment:规范化PNG明文字节数" json:"-"`
	Width            int             `gorm:"column:width;not null;comment:规范化图片像素宽度" json:"-"`
	Height           int             `gorm:"column:height;not null;comment:规范化图片像素高度" json:"-"`
	ScanState        string          `gorm:"column:scan_state;not null;default:pending;comment:图片安全扫描状态" json:"-"`
	UploadCipher     json.RawMessage `gorm:"column:upload_cipher;type:jsonb;comment:待上传的稳定密文，完成后清空" json:"-"`
	UploadUntil      *time.Time      `gorm:"column:upload_until;type:timestamptz;comment:外部上传排他预约期限" json:"-"`
	RemovableAfter   time.Time       `gorm:"column:removable_after;type:timestamptz;not null;comment:未引用图片允许清理的最早时间" json:"-"`
	CleanupStartedAt *time.Time      `gorm:"column:cleanup_started_at;type:timestamptz;comment:对象已被清理器认领，禁止重新引用" json:"-"`
	InstalledAt      *time.Time      `gorm:"column:installed_at;type:timestamptz;comment:审核及上传完成时间" json:"-"`
	RemovedAt        *time.Time      `gorm:"column:removed_at;type:timestamptz;comment:外部对象删除确认时间" json:"-"`
}

// TableName 返回图片对象表。
func (WishImage) TableName() string { return "public.wish_images" }
