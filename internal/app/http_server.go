package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// closeServerResource 让清理失败影响宿主退出状态，同时继续执行其余逆序清理。
// 只保留固定操作名和安全错误分类，底层连接错误可能带有凭据或地址。
func closeServerResource(log *zap.Logger, operation string, close func() error, result *error) {
	start := time.Now()
	var err error
	defer finish(log, operation, start, &err)
	if close() != nil {
		err = fmt.Errorf("%s：%w", operation, command.ErrUnavailable)
		*result = errors.Join(*result, err)
	}
}

// serveHTTP 绑定成功后才报告启动，收到取消后先排空请求再允许宿主释放业务依赖。
func serveHTTP(ctx context.Context, server *http.Server, shutdownTimeout time.Duration, log *zap.Logger) error {
	// 先绑定监听地址再记录启动成功，避免地址冲突时误报就绪。
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Error("服务启动失败", zap.Error(err))
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Info("服务启动完成", zap.String("address", listener.Addr().String()))
	// 存活心跳独立于业务ready，停止函数等待日志协程退出。
	stopHeartbeat := platform.StartHeartbeat(ctx, log, "api")
	defer stopHeartbeat()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("服务运行失败", zap.Error(err))
			return err
		}
		return nil
	case <-ctx.Done():
	}
	// 先等待 HTTP 请求结束，再由延迟清理逻辑关闭共享客户端。
	log.Info("开始关闭服务")
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		log.Error("服务关闭失败", zap.Error(err))
		return err
	}
	log.Info("服务关闭完成")
	return nil
}
