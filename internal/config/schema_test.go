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
			if c.Environment != environment || !c.Redis.Enabled || c.Database.Driver != "postgres" {
				t.Fatal("环境或共享限流启用配置丢失")
			}
			if c.Business.Runtime.ResourcesFile != "resources.yaml" || c.Business.Runtime.Tasks.ScanInterval != time.Second ||
				c.Business.Runtime.Tasks.OperationTimeout != 100*time.Second || c.Business.Runtime.Tasks.Consumer.Timeout != 5*time.Second {
				t.Fatal("公共资源或任务预算的层级不符")
			}
			if c.Business.Card.MaxBytes != 4<<20 || c.Business.Card.Lease != 90*time.Second ||
				c.Business.Runtime.ProviderTimeout != 30*time.Second ||
				c.Server.ReadTimeout != 45*time.Second || c.Server.WriteTimeout != 55*time.Second {
				t.Fatal("学生卡图片或请求预算不符")
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

// TestFoundationTemplate 验证仓库公共配置可直接加载为不依赖中间件的纯健康模式。
func TestFoundationTemplate(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "configs", "common.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "foundation" || c.Database.Driver != "" || c.Database.DSN != "" || c.Redis.Enabled {
		t.Fatal("基础模式启用了中间件")
	}
}
