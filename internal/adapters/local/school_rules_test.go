package local

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestDevelopmentSchoolRules 保留 T011/REQ-002/015/018：无服务直接通过、取消与日志脱敏。
func TestDevelopmentSchoolRules(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	Logger := zap.New(core)
	rules := SchoolRules{Logger}
	if err := rules.Validate(context.Background(), "PRIVATE_NUMBER", "PRIVATE_NAME"); err != nil {
		t.Fatal("无服务策略应直接通过")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rules.Validate(ctx, "PRIVATE_NUMBER", "PRIVATE_NAME"); !errors.Is(err, context.Canceled) {
		t.Fatal("取消后不能报告通过")
	}
	entries := logs.All()
	if len(entries) != 2 {
		t.Fatal("缺少完成日志")
	}
	for i, entry := range entries {
		fields := entry.ContextMap()
		want := "success"
		if i == 1 {
			want = "failure"
		}
		if fields["external_service"] != false || fields["result"] != want || fields["operation"] != "school_rules_bypass" ||
			fields["duration"] == nil {
			t.Fatal("完成日志字段不完整")
		}
		for _, field := range entry.Context {
			if strings.Contains(field.String, "PRIVATE_") {
				t.Fatal("日志泄漏校验输入")
			}
		}
	}
}
