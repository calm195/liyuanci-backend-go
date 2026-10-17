package worker

import (
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// TestProbeIsolation 验证 T004/REQ-018、020：无真实依赖或生产环境不启动测试消费者。
func TestProbeIsolation(t *testing.T) {
	for _, env := range []string{"foundation", "production", "integration"} {
		c := config.Config{Environment: env}
		if _, err := Start(c, &platform.Resources{}, zap.NewNop()); err == nil {
			t.Fatal("probe isolation bypass")
		}
	}
	for _, id := range []string{"", "identity-or-body", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", "0000000000000000000000000000000g"} {
		if validID(id) {
			t.Fatal("unsafe identifier accepted")
		}
	}
	if !validID("0123456789abcdef0123456789abcdef") {
		t.Fatal("valid random identifier rejected")
	}
}
