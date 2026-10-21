package platform

import (
	"context"
	"testing"
)

// TestMissingMiddlewareIsNotReady 验证 T004/REQ-020：存活不能冒充真实依赖就绪。
func TestMissingMiddlewareIsNotReady(t *testing.T) {
	r := &Resources{}
	if r.Ready(context.Background()) == nil {
		t.Fatal("missing middleware reported ready")
	}
	if err := r.Close(); err != nil {
		t.Fatal("empty resources cleanup failed")
	}
}
