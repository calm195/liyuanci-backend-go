package app

import (
	"os"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
)

// developmentMedia 保持开发API与worker使用同一腾讯云配置；本地文件和模拟扫描仅留给隔离集成。
func developmentMedia(environment string, cfg DevelopmentSettings, logger *zap.Logger) (Objects, studentcard.Scanner, func(), error) {
	if cfg.Tencent.Enabled {
		client, err := openObjectGateway(RuntimeSettings{Tencent: cfg.Tencent, ProviderTimeout: cfg.ProviderTimeout}, 2*(1<<20)+65536, logger)
		if err != nil {
			return nil, nil, nil, err
		}
		return client, imageScanner(client), client.Close, nil
	}
	if environment != "integration" {
		return unavailableObjects{}, unavailableScanner{}, func() {}, nil
	}
	if info, err := os.Stat(cfg.ObjectDirectory); err != nil || !info.IsDir() {
		return nil, nil, nil, command.ErrUnavailable
	}
	return &local.Objects{Root: cfg.ObjectDirectory, Logger: logger}, local.Scanner{Logger: logger}, func() {}, nil
}
