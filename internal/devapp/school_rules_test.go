package devapp

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestDevelopmentAssemblyRejectsMissingDependencies 确认开发装配需要显式环境和依赖。
func TestDevelopmentAssemblyRejectsMissingDependencies(t *testing.T) {
	logger := zap.NewNop()
	for _, environment := range []string{"production", "development"} {
		if _, _, err := Open(context.Background(), environment, Settings{}, nil, logger, nil); err == nil {
			t.Fatal("非隔离环境必须拒绝开发装配")
		}
	}
}
