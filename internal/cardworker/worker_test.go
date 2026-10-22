package cardworker

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
)

// TestOpenRejectsMissingDependencies 验证学生卡宿主适配仍拒绝缺失依赖。
func TestOpenRejectsMissingDependencies(t *testing.T) {
	if _, err := Open(context.Background(), nil, nil, zap.NewNop(), Config{}); err != durable.ErrInvalid {
		t.Fatal("缺依赖仍启动")
	}
}
