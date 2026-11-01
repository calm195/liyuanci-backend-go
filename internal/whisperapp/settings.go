// Package whisperapp 装配树洞及完整业务宿主，共用正式供应商、最小权限和恢复边界，不隐式迁移或生成业务资料。
package whisperapp

import "github.com/calm195/liyuanci-backend-go/internal/config"

// Settings 是配置边界提供的只读运行约束，所有文件加载由config负责。
type Settings = config.Whispers

// ResourcesFile 是配置边界提供的版本化公共资源。
type ResourcesFile = config.WhisperResources
