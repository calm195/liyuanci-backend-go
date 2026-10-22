package devapp

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestDevelopmentCardWorkerBoundary 保留 T011/REQ-015/018 开发模拟与生产隔离追溯。
func TestDevelopmentCardWorkerBoundary(t *testing.T) {
	logger := zap.NewNop()
	if err := RunCardWorker(context.Background(), "production", Settings{WorkerDSN: "unused"}, logger); err == nil {
		t.Fatal("生产环境必须拒绝模拟扫描")
	}
	scanner := developmentScanner{logger}
	if ok, err := scanner.Scan(context.Background(), nil, "image/png"); !ok || err != nil {
		t.Fatal("开发扫描应模拟通过")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := scanner.Scan(ctx, nil, "image/png"); ok || err == nil {
		t.Fatal("取消后不能报告通过")
	}
}
