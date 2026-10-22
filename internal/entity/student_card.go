package entity

import "encoding/json"

// StudentCardPayload 保存等待外部持久化的信封密文；外部成功后清除，不存原图明文。
type StudentCardPayload struct {
	AssetID  string          `gorm:"column:asset_id;type:uuid;primaryKey;autoIncrement:false;comment:对应学生卡对象，物理清理时级联移除暂存密文" json:"-"`
	Envelope json.RawMessage `gorm:"column:envelope;type:jsonb;not null;comment:学生卡图片的认证信封密文，不包含明文数据密钥" json:"-"`
	MIME     string          `gorm:"column:mime;type:text;not null;comment:重新编码后的图片媒体类型" json:"-"`
}

// TableName 显式限定暂存表，避免search_path影响定位。
func (StudentCardPayload) TableName() string { return "public.student_card_payloads" }
