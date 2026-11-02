// Package gateway 实现正式HTTPS供应商协议；不读取应用配置，也不装配业务服务。
package gateway

import "time"

// Config 只描述网关传输参数，由应用装配层从已校验配置显式映射。
// Token和CA来自私有YAML；适配器不读取环境变量、文件或打印这些值。
type Config struct {
	URL, Token, CA, Version string
	Timeout                 time.Duration
}
