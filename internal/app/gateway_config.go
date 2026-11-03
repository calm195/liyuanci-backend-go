package app

import (
	"context"
	"io"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
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
func openObjectGateway(cfg WhisperSettings, maxBytes int64, log *zap.Logger) (objectGateway, error) {
	if cfg.Tencent.Enabled {
		return gateway.NewTencent(gateway.TencentConfig{
			Bucket:         cfg.Tencent.Bucket,
			ObjectURL:      cfg.Tencent.ObjectURL,
			ImageURL:       cfg.Tencent.ImageURL,
			CA:             cfg.Tencent.CA,
			Region:         cfg.Tencent.Region,
			SecretID:       cfg.Tencent.SecretID,
			SecretKey:      cfg.Tencent.SecretKey,
			SessionToken:   cfg.Tencent.SessionToken,
			BizType:        cfg.Tencent.BizType,
			Timeout:        cfg.ProviderTimeout,
			MaxObjectBytes: maxBytes,
		}, log)
	}
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
func objectStore(g objectGateway) Objects {
	if g == nil {
		return unavailableObjects{}
	}
	return g
}

// objectGateway 让装配层统一拥有两种对象协议的生命周期。
type objectGateway interface {
	Objects
	Close()
}

// imageScanner 未配置CI时失败关闭，正式链路不得借用开发直接通过适配器。
func imageScanner(g objectGateway) studentcard.Scanner {
	if scanner, ok := g.(*gateway.Tencent); ok && scanner != nil {
		return scanner
	}
	return unavailableScanner{}
}

type unavailableScanner struct{}

func (unavailableScanner) Scan(context.Context, []byte, string) (bool, error) {
	return false, command.ErrUnavailable
}
