package config

import "time"

// Command 保存命令事务预算，应用装配时转换为领域配置，不依赖事务实现包。
type Command struct {
	Timeout     time.Duration
	LockTimeout time.Duration
	ReceiptTTL  time.Duration
	MaxAttempts int
}

// Consumer 保存持久任务预算，字段与既有YAML保持一致。
type Consumer struct {
	Timeout     time.Duration
	LockTimeout time.Duration
	RetryDelay  time.Duration
	MaxAttempts int
}

// CareResources 保存经运营核验的关怀资料；可用性仍由业务构造器复核。
type CareResources struct {
	Version        string `json:"version"`
	Source         string `json:"source"`
	VerifiedAt     string `json:"verified_at"`
	Hotline24Hours string `json:"hotline_24_hours"`
	Appointment    string `json:"appointment"`
	Notice         string `json:"notice"`
}

// Catalog 保存资源文件的原始结构，不在配置读取时建立业务投影。
type Catalog struct {
	Grades  Dictionary
	Reports []DictionaryItem
	Help    HelpDirectory
}

// Dictionary 保存可选版本和字典条目，缺省版本与零版本保持区别。
type Dictionary struct {
	Version *int64           `json:"version"`
	Items   []DictionaryItem `json:"items"`
}

// DictionaryItem 保存配置中的稳定代码与展示名称。
type DictionaryItem struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// HelpDirectory 保存援助资料及核验状态，缺失内容不会自动填入示例渠道。
type HelpDirectory struct {
	Status     string     `json:"status"`
	Version    *int64     `json:"version"`
	VerifiedAt *string    `json:"verified_at"`
	Items      []HelpItem `json:"items"`
}

// HelpItem 保留资源文件的字段标记，尤其source_url不能随Go名称变化。
type HelpItem struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Contact   string `json:"contact"`
	SourceURL string `json:"source_url"`
}
