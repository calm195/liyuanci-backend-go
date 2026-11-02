// Package config 读取严格分层YAML并校验运行参数，不依赖业务服务、HTTP或应用装配。
// 派生密钥保留既有用途协议；业务参数由app显式转换，业务准入仍由对应构造器复核。
package config

import "errors"

var (
	// ErrInvalid 表示配置字段或跨字段约束无效，错误不包含秘密值。
	ErrInvalid = errors.New("配置参数无效")
	// ErrForbidden 表示所选环境不允许启用该运行模式。
	ErrForbidden = errors.New("配置模式不允许在当前环境启用")
	// ErrUnavailable 表示必需资源资料缺失或无法使用。
	ErrUnavailable = errors.New("配置资源不可用")
)
