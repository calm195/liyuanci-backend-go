package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/cardworker"
	"github.com/calm195/liyuanci-backend-go/internal/config"
)

// TestCardWorkerMQConfig 验证 T007 显式配置、未知字段拒绝与启动前预算校验。
func TestCardWorkerMQConfig(t *testing.T) {
	sample, err := os.ReadFile("../../configs/examples/card-worker-mq.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text   string
		parse, valid bool
	}{
		{"显式开发模式", string(sample), true, true},
		{"未知字段", string(sample) + "unknown: true\n", false, false},
		{"预算为零", strings.Replace(string(sample), "sendtimeout: 3s", "sendtimeout: 0s", 1), true, false},
		{"探针主题", strings.Replace(string(sample), "liyuanci_student_card_scan_v1", "liyuanci_foundation_probe_v1", 1), true, false},
		{"缺身份", strings.Replace(string(sample), "allowunauthenticated: true", "allowunauthenticated: false", 1), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mq.yaml")
			if os.WriteFile(path, []byte(tc.text), 0600) != nil {
				t.Fatal("写配置失败")
			}
			var cfg cardworker.Config
			err := config.LoadCardWorkerMQ(path, &cfg)
			if (err == nil) != tc.parse {
				t.Fatalf("配置解析结果不符：%v", err)
			}
			if err == nil && (cardworker.Validate(cfg) == nil) != tc.valid {
				t.Fatal("无效预算或通道仍可启动")
			}
		})
	}
	if config.LoadCardWorkerMQ("", nil) == nil {
		t.Fatal("缺文件不能启用消息模式")
	}
}
