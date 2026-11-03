package app

import "github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"

// gatewayConfig 只交付供应商需要的参数，避免适配器持有应用数据库和派生密钥配置。
func gatewayConfig(cfg WhisperSettings) gateway.Config {
	return gateway.Config{
		URL:     cfg.GatewayURL,
		Token:   cfg.GatewayToken,
		CA:      cfg.GatewayCA,
		Version: cfg.ProviderVersion,
		Timeout: cfg.ProviderTimeout,
	}
}
