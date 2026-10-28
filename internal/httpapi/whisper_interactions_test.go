package httpapi

import (
	"strings"
	"testing"
)

// TestCommentInputBoundary 验证互动输入复用严格身份、码点与无隐式归一化边界。
func TestCommentInputBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		update, valid bool
	}{
		{"创建", `{"expected_post_version":1,"expected_comments_epoch":2,"body":" 原文 ","identity":{"mode":"anonymous"}}`, false, true},
		{"改稿", `{"expected_version":3,"expected_post_version":1,"expected_comments_epoch":2,"body":"正文","identity":{"mode":"anonymous"}}`, true, true},
		{"缺本人版本", `{"expected_post_version":1,"expected_comments_epoch":2,"body":"正文","identity":{"mode":"anonymous"}}`, true, false},
		{"零父版本", `{"expected_post_version":0,"expected_comments_epoch":2,"body":"正文","identity":{"mode":"anonymous"}}`, false, false},
		{"重复代次", `{"expected_post_version":1,"expected_comments_epoch":2,"expected_comments_epoch":3,"body":"正文","identity":{"mode":"anonymous"}}`, false, false},
		{"显式空值", `{"expected_post_version":1,"expected_comments_epoch":2,"body":null,"identity":{"mode":"anonymous"}}`, false, false},
		{"额外字段", `{"expected_post_version":1,"expected_comments_epoch":2,"body":"正文","identity":{"mode":"anonymous"},"post_id":"注入"}`, false, false},
		{"坏代理项", `{"expected_post_version":1,"expected_comments_epoch":2,"body":"\ud800","identity":{"mode":"anonymous"}}`, false, false},
		{"作者注入", `{"expected_post_version":1,"expected_comments_epoch":2,"body":"正文","identity":{"mode":"anonymous","author_id":"注入"}}`, false, false},
		{"码点边界", `{"expected_post_version":1,"expected_comments_epoch":2,"body":"` + strings.Repeat("鲤", 500) + `","identity":{"mode":"anonymous"}}`, false, true},
		{"超长", `{"expected_post_version":1,"expected_comments_epoch":2,"body":"` + strings.Repeat("鲤", 501) + `","identity":{"mode":"anonymous"}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, e := decodeWhisperComment([]byte(tc.body), tc.update)
			if (e == nil) != tc.valid {
				t.Fatal(v, e)
			}
			if tc.name == "创建" && v.Body != " 原文 " {
				t.Fatal("原文被修改")
			}
		})
	}
}
