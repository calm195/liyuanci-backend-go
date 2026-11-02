package app

import "github.com/calm195/liyuanci-backend-go/internal/config"

// WhisperSettings 是配置边界提供的只读运行约束，所有文件加载由config负责。
type WhisperSettings = config.Whispers

// ResourcesFile 是配置边界提供的版本化公共资源。
type ResourcesFile = config.WhisperResources
