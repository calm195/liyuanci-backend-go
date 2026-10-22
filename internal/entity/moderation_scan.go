package entity

import "encoding/json"

// ModerationScan 保存已完成扫描的不可变规则来源及加密候选。
type ModerationScan struct {
	TaskID           string          `gorm:"column:task_id;type:uuid;primaryKey;comment:本轮精确审核案件引用" json:"-"`
	CrisisVersion    string          `gorm:"column:crisis_version;type:uuid;not null;comment:本次使用的危机规则快照" json:"-"`
	SchoolVersion    string          `gorm:"column:school_version;type:uuid;not null;comment:本次使用的涉校规则快照" json:"-"`
	OrdinaryVersion  string          `gorm:"column:ordinary_version;type:uuid;not null;comment:本次使用的普通违规规则快照" json:"-"`
	DetectorVersion  string          `gorm:"column:detector_version;type:text;not null;comment:文本安全与组合信息处理器版本" json:"-"`
	SchoolRelated    bool            `gorm:"column:school_related;type:boolean;not null;comment:当前版本涉校判定结果" json:"-"`
	OrdinaryHit      bool            `gorm:"column:ordinary_hit;type:boolean;not null;comment:普通规则或文本安全处理器要求人工复核" json:"-"`
	CrisisHit        bool            `gorm:"column:crisis_hit;type:boolean;not null;comment:危机命中，只能在危机专用范围处理" json:"-"`
	ProjectionCipher json.RawMessage `gorm:"column:projection_cipher;type:jsonb;not null;comment:待放行脱敏正文与标签的加密候选，不向列表公开" json:"-"`
}

// TableName 返回显式扫描快照表。
func (ModerationScan) TableName() string { return "public.moderation_scans" }
