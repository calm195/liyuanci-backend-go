// Package testconfig 为隔离集成测试提供显式YAML输入，未指定配置时保留单元测试的跳过边界。
package testconfig

import (
	"flag"
	"sync"

	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

var path = flag.String("integration-config", "", "隔离集成测试YAML；不读取环境变量")
var once sync.Once
var values map[string]string

// Value 读取测试资源引用；显式文件错误必须失败，不能被误报为缺依赖跳过。
func Value(name string) string {
	once.Do(func() {
		values = map[string]string{}
		if *path != "" && yamlconfig.Read(*path, &values) != nil {
			panic("显式集成测试YAML无效")
		}
	})
	return values[name]
}
