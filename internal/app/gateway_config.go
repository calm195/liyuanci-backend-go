package app

import (
	"context"
	"io"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

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

// openObjectGateway 仅在显式配置对象端点时创建客户端，不在启动时要求供应商已选定。
func openObjectGateway(cfg WhisperSettings, maxBytes int64, log *zap.Logger) (*gateway.Business, error) {
	if cfg.GatewayURL == "" && cfg.GatewayToken == "" && cfg.GatewayCA == "" && cfg.ProviderVersion == "" {
		return nil, nil
	}
	g, err := gateway.New(gatewayConfig(cfg), log)
	if err != nil {
		return nil, err
	}
	return gateway.NewBusiness(g, maxBytes), nil
}

type unavailableObjects struct{}

func (unavailableObjects) PutOnce(context.Context, string, []byte) (string, error) {
	return "", command.ErrUnavailable
}

func (unavailableObjects) Open(context.Context, string, string) (io.ReadCloser, error) {
	return nil, command.ErrUnavailable
}

// objectStore 避免将带nil指针的接口注入业务服务；未配置对象服务时读写明确不可用。
func objectStore(g *gateway.Business) Objects {
	if g == nil {
		return unavailableObjects{}
	}
	return g
}
