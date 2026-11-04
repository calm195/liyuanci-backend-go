package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/config"
)

// TestResourceConfigurationCompatibility 用既有YAML字段验证配置结构与领域投影的转换。
// 同时检查空数组、缺省字段和拷贝隔离，避免结构解耦改变客户端可见的资料状态。
func TestResourceConfigurationCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.yaml")
	raw := `version: synthetic-v1
catalog:
  grades:
    version: 7
    items:
      - code: grade_test
        label: 合成年级
  reports: []
  help:
    status: ready
    version: 8
    verified_at: '2026-11-02'
    items:
      - kind: school_appointment
        label: 合成预约
        contact: 仅用于测试
        source_url: https://example.invalid/source
care:
  version: synthetic-care-v1
  source: 隔离测试资料
  verified_at: '2026-11-02'
  hotline_24_hours: 仅用于测试
  appointment: 合成预约
  notice: 不是真实援助信息
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := config.LoadResources(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := readResources(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != "synthetic-v1" || out.Catalog.Grades.Version == nil || *out.Catalog.Grades.Version != 7 ||
		len(out.Catalog.Grades.Items) != 1 || out.Catalog.Grades.Items[0].Code != "grade_test" || out.Catalog.Grades.Items[0].Label != "合成年级" ||
		out.Catalog.Reports == nil || len(out.Catalog.Reports) != 0 ||
		out.Catalog.Help.Version == nil || *out.Catalog.Help.Version != 8 || out.Catalog.Help.VerifiedAt == nil || *out.Catalog.Help.VerifiedAt != "2026-11-02" ||
		len(out.Catalog.Help.Items) != 1 || out.Catalog.Help.Items[0].SourceURL != "https://example.invalid/source" ||
		out.Care == nil || out.Care.Hotline24Hours != "仅用于测试" || out.Care.Source != "隔离测试资料" {
		t.Fatal("资源文件字段或空值语义发生变化")
	}
	copy := resourceSnapshot(source)
	*copy.Catalog.Grades.Version = 99
	copy.Catalog.Grades.Items[0].Label = "已修改"
	*copy.Catalog.Help.Version = 99
	*copy.Catalog.Help.VerifiedAt = "已修改"
	copy.Catalog.Help.Items[0].Contact = "已修改"
	copy.Care.Notice = "已修改"
	if !reflect.DeepEqual(resourceSnapshot(source), out) {
		t.Fatal("业务投影修改污染了原始配置")
	}
	empty := resourceSnapshot(config.Resources{Version: "empty"})
	if empty.Care != nil || empty.Catalog.Grades.Version != nil || empty.Catalog.Grades.Items != nil || empty.Catalog.Reports != nil ||
		empty.Catalog.Help.Items != nil ||
		empty.Catalog.Help.VerifiedAt != nil {
		t.Fatal("缺省资料被补成可用内容")
	}
}
