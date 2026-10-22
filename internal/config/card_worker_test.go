package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/cardworker"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
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

// TestContentWorkerMQConfig 验证 T016 独立通道与完整分析预算，拒绝跨用学生卡事件。
func TestContentWorkerMQConfig(t *testing.T) {
	sample, err := os.ReadFile("../../configs/examples/content-worker-mq.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"独立通道", string(sample), true},
		{"学生卡事件", strings.Replace(string(sample), "wish.review_requested", "student_card.scan_requested", 1), false},
		{"短调度预算", strings.Replace(string(sample), "operationtimeout: 20s", "operationtimeout: 1s", 1), false},
		{"短消费预算", strings.Replace(string(sample), "handlertimeout: 20s", "handlertimeout: 1s", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mq.yaml")
			if os.WriteFile(path, []byte(tc.text), 0600) != nil {
				t.Fatal("写配置失败")
			}
			var cfg workrunner.Config
			if config.LoadCardWorkerMQ(path, &cfg) != nil {
				t.Fatal("解析配置失败")
			}
			settings := devapp.Settings{WorkerDSN: "unused", WrapKey: make([]byte, 32), LookupKey: make([]byte, 32)}
			if (devapp.ValidateContentWorker("integration", settings, devapp.ContentWorkerConfig{MQ: &cfg}) == nil) != tc.valid {
				t.Fatal("内容通道预算无效")
			}
			if tc.valid && workrunner.Validate(cfg, wish.ReviewRoute()) != nil {
				t.Fatal("有效配置被拒绝")
			}
		})
	}
}
