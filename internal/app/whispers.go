package app

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// WhisperAPI 组合独立树洞入口所需的运行资源与服务，宿主先排空请求再关闭。
type WhisperAPI struct {
	runtime  *apiRuntime
	services *whisperServices
}

// OpenWhispers 在监听前检查资源与恢复门禁并构造树洞服务，不启动后台任务。
func OpenWhispers(ctx context.Context, cfg RuntimeSettings, client redis.UniversalClient, log *zap.Logger) (out *WhisperAPI, err error) {
	runtime, services, err := openAPIComponents(ctx, cfg, client, log)
	if err != nil {
		return nil, err
	}
	return &WhisperAPI{runtime: runtime, services: services}, nil
}

// Register 一次注册树洞及配套治理入口，失败时宿主须丢弃router。
func (a *WhisperAPI) Register(router *gin.Engine) error { return a.services.register(router) }

// Ready 先检查共享资源，再复核树洞配置与资料。
func (a *WhisperAPI) Ready(ctx context.Context) error {
	if err := a.runtime.ready(ctx); err != nil {
		return err
	}
	return a.services.ready(ctx)
}

// Close 只释放本API拥有的资源，不关闭宿主借出的Redis。
func (a *WhisperAPI) Close() error { return a.runtime.close() }
