package dictionary

import (
	"strings"
	"testing"
)

func TestDisplayValidation(t *testing.T) {
	enabled, disabled := true, false
	for _, test := range []struct {
		name, kind, code string
		input            SaveInput
		valid            bool
	}{
		{"允许中文名称", "resource_feedback", "valid", SaveInput{1, "有帮助", 0, &enabled}, true},
		{"允许停用反馈选项", "resource_feedback", "valid", SaveInput{1, "有帮助", 0, &disabled}, true},
		{"拒绝停用系统状态", "verification_status", "verified", SaveInput{1, "通过", 0, &disabled}, false},
		{"拒绝省略启用字段", "resource_feedback", "valid", SaveInput{1, "有帮助", 0, nil}, false},
		{"拒绝空白名称", "resource_feedback", "valid", SaveInput{1, " ", 0, &enabled}, false},
		{"拒绝首尾空格", "resource_feedback", "valid", SaveInput{1, " 有帮助", 0, &enabled}, false},
		{"拒绝注入路径", "../feedback", "valid", SaveInput{1, "有帮助", 0, &enabled}, false},
		{"拒绝无效版本", "resource_feedback", "valid", SaveInput{0, "有帮助", 0, &enabled}, false},
		{"拒绝负排序", "resource_feedback", "valid", SaveInput{1, "有帮助", -1, &enabled}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if validInput(test.kind, test.code, test.input) != test.valid {
				t.Fatal("展示字典字段边界不符合预期")
			}
		})
	}
}

func TestReportTypeCodeBoundary(t *testing.T) {
	published := true
	input := SaveReportTypeInput{ExpectedVersion: 1, Label: "举报类型", Published: &published}
	for _, test := range []struct {
		code  string
		valid bool
	}{
		{"Legacy-Code_1", true}, {"1-other", true}, {strings.Repeat("a", 32), true}, {strings.Repeat("a", 33), false}, {"", false}, {"../other", false},
	} {
		if validReportTypeInput(test.code, input) != test.valid {
			t.Fatalf("举报类型代码校验错误：%q", test.code)
		}
	}
}
