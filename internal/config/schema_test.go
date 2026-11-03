package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// TestTemplateSchema 验证公共配置和覆盖模板与Go结构一致，任务层级及Redis启用不能在精简时丢失。
func TestTemplateSchema(t *testing.T) {
	for _, environment := range []string{"development", "production"} {
		t.Run(environment, func(t *testing.T) {
			c := Defaults()
			root := filepath.Join("..", "..", "configs")
			if err := yamlconfig.ReadLayers([]string{
				filepath.Join(root, "common.yaml"),
				filepath.Join(root, environment+".example.yaml"),
			}, &c); err != nil {
				t.Fatal(err)
			}
			if c.Environment != environment || !c.Redis.Enabled {
				t.Fatal("环境或共享限流启用配置丢失")
			}
			if c.Business.Runtime.ResourcesFile != "resources.yaml" || c.Business.Runtime.Tasks.ScanInterval != time.Second ||
				c.Business.Runtime.Tasks.OperationTimeout != time.Minute || c.Business.Runtime.Tasks.Consumer.Timeout != 5*time.Second {
				t.Fatal("公共资源或任务预算的层级不符")
			}
		})
	}
}

// TestRetiredConfigurationRejected 验证弃用字段在两个运行块中均失败关闭，不静默接受无效覆盖。
func TestRetiredConfigurationRejected(t *testing.T) {
	for _, block := range []string{"business:\n  runtime:\n", "whispers:\n"} {
		indent := "  "
		if block[0] == 'b' {
			indent = "    "
		}
		for _, field := range []string{
			"gatewayurl: ''", "gatewaytoken: ''", "gatewayca: ''", "providerversion: ''",
			"catalogfile: resources.yaml", "worker: {}", "interval: 1s", "operationtimeout: 1m",
			"contact: ''", "care: {}",
		} {
			t.Run(block+field, func(t *testing.T) {
				if _, err := Load(fixture(t, "mode: foundation\n", block+indent+field+"\n")); err == nil {
					t.Fatal("接受已退役字段")
				}
			})
		}
	}
	if _, err := Load(fixture(t, "mode: foundation\n", "worker:\n  contentanalyzer: manual\n")); err == nil {
		t.Fatal("接受已删除的审核模式选项")
	}
}
