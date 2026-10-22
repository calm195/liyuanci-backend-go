package devapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestDevelopmentSchoolRules 保留 T011/REQ-002/015/018：开发模拟、取消、日志脱敏和生产入口拒绝。
func TestDevelopmentSchoolRules(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	rules := developmentSchoolRules{logger}
	if err := rules.Validate(context.Background(), "PRIVATE_NUMBER", "PRIVATE_NAME"); err != nil {
		t.Fatal("开发规则应模拟通过")
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
		if fields["simulated"] != true || fields["result"] != want || fields["operation"] != "development_school_rules" || fields["duration"] == nil {
			t.Fatal("模拟完成日志字段不完整")
		}
		for _, field := range entry.Context {
			if strings.Contains(field.String, "PRIVATE_") {
				t.Fatal("日志泄漏校验输入")
			}
		}
	}
	for _, environment := range []string{"production", "development"} {
		if _, _, err := Open(context.Background(), environment, Settings{}, nil, logger, nil); err == nil {
			t.Fatal("非隔离环境必须拒绝开发装配")
		}
	}
}
