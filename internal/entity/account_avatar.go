package entity

import "time"

// AccountAvatar 记录本人头像的独立私有对象及持久清理状态。
type AccountAvatar struct {
	ID               string     `gorm:"column:id;type:uuid;primaryKey;autoIncrement:false;comment:头像随机标识" json:"-"`
	OwnerID          *string    `gorm:"column:owner_id;type:uuid;comment:头像所属账号，注销后置空并保留清理对象引用" json:"-"`
	ObjectKey        string     `gorm:"column:object_key;type:text;not null;unique;comment:独立头像对象键" json:"-"`
	ObjectVersion    string     `gorm:"column:object_version;type:text;not null;comment:规范化头像的SHA-256完整性版本" json:"-"`
	ByteSize         int64      `gorm:"column:byte_size;type:bigint;not null;comment:规范化PNG的字节数" json:"-"`
	RemovableAfter   time.Time  `gorm:"column:removable_after;type:timestamptz;not null;comment:允许尝试清理的最早时间，保护在途上传" json:"-"`
	UploadUntil      *time.Time `gorm:"column:upload_until;type:timestamptz;comment:外部上传排他预约期限" json:"-"`
	CleanupStartedAt *time.Time `gorm:"column:cleanup_started_at;type:timestamptz;comment:已进入清理，禁止重启上传" json:"-"`
	InstalledAt      *time.Time `gorm:"column:installed_at;type:timestamptz;comment:曾安装时间，保留幂等重放判断" json:"-"`
	RemovedAt        *time.Time `gorm:"column:removed_at;type:timestamptz;comment:对象清理确认时间" json:"-"`
}

// TableName 限定头像表所在的业务模式。
func (AccountAvatar) TableName() string { return "public.account_avatars" }
