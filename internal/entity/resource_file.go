package entity

import "time"

// ResourceFile 保存资料私有文件、上传预约和清理状态。
type ResourceFile struct {
	ID               string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:资料文件标识" json:"-"`
	OwnerID          string     `gorm:"column:owner_id;type:uuid;not null;comment:上传管理员" json:"-"`
	ObjectKey        string     `gorm:"column:object_key;type:text;not null;unique;comment:私有对象键" json:"-"`
	Name             string     `gorm:"column:name;type:text;not null;comment:原始文件名称" json:"-"`
	ContentType      string     `gorm:"column:content_type;type:text;not null;comment:文件媒体类型" json:"-"`
	ByteSize         int64      `gorm:"column:byte_size;type:bigint;not null;comment:文件字节数" json:"-"`
	SHA256           string     `gorm:"column:sha256;type:text;not null;comment:文件内容摘要" json:"-"`
	Ready            bool       `gorm:"column:ready;type:boolean;not null;comment:上传完整性已确认" json:"-"`
	CreatedAt        time.Time  `gorm:"column:created_at;type:timestamptz;not null;comment:上传预约时间" json:"-"`
	RemovableAfter   time.Time  `gorm:"column:removable_after;type:timestamptz;not null;comment:允许清理未引用文件的时间" json:"-"`
	UploadUntil      *time.Time `gorm:"column:upload_until;type:timestamptz;comment:上传预约到期时间" json:"-"`
	CleanupStartedAt *time.Time `gorm:"column:cleanup_started_at;type:timestamptz;comment:清理开始时间，禁止再次绑定" json:"-"`
	RemovedAt        *time.Time `gorm:"column:removed_at;type:timestamptz;comment:对象已确认删除时间" json:"-"`
}

// TableName 返回资料文件表。
func (ResourceFile) TableName() string { return "public.resource_files" }
