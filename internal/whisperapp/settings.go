// Package whisperapp 装配树洞正式入口及显式外部依赖，不拥有迁移或默认业务资料。
package whisperapp

import "github.com/calm195/liyuanci-backend-go/internal/config"

// Settings 是配置边界提供的只读运行约束，所有文件加载由config负责。
type Settings = config.Whispers

// ResourcesFile 是配置边界提供的版本化公共资源。
type ResourcesFile = config.WhisperResources
