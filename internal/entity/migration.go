package entity

import (
	"time"
)

// SchemaMigration 映射 public.schema_migrations，字段注释描述数据库持久化语义。
type SchemaMigration struct {
	Version   int64     `gorm:"column:version;type:bigint;primaryKey;autoIncrement:false;comment:迁移版本" json:"-"`
	Checksum  string    `gorm:"column:checksum;type:text;not null;comment:迁移校验和" json:"-"`
	AppliedAt time.Time `gorm:"column:applied_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP;comment:迁移应用时间" json:"-"`
}

// TableName 返回显式 schema 与表名，避免 search_path 影响实体定位。
func (SchemaMigration) TableName() string { return "public.schema_migrations" }
