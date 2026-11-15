package entity

import "time"

// ConversationRead 仅保存本人已展示的送达位置，不向另一参与者暴露阅读记录。
type ConversationRead struct {
	ConversationID string    `gorm:"column:conversation_id;type:uuid;primaryKey;autoIncrement:false;comment:所属会话" json:"-"`
	AccountID      string    `gorm:"column:account_id;type:uuid;primaryKey;autoIncrement:false;comment:阅读者账号" json:"-"`
	LastReadSeq    int64     `gorm:"column:last_read_seq;type:bigint;not null;comment:本人已展示的最大送达序号，只增不减" json:"-"`
	UpdatedAt      time.Time `gorm:"column:updated_at;type:timestamptz;not null;autoCreateTime:false;autoUpdateTime:false;comment:阅读位置最近推进时间" json:"-"`
}

func (ConversationRead) TableName() string { return "public.conversation_reads" }
