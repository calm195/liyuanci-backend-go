package moderation

import (
	"strings"
	"testing"
)

// TestWordlistSourcesAndOffsets 验证归一化合并仍保留两个来源、原词和 UTF-16 位置。
func TestWordlistSourcesAndOffsets(t *testing.T) {
	snapshot := rules{
		crisisEntries: []string{"ＡＢ"},
		schoolEntries: []string{"ab"},
		builtinEntries: []builtinRule{
			{Word: "ab", Label: "upstream_review", Disposition: "review", Enabled: true},
			{Word: "ＡＢ", Label: "upstream_hint", Disposition: hintDisposition, Enabled: true},
			{Word: "ab", Label: "upstream_disabled", Disposition: "review", Enabled: false},
		},
	}
	out, err := scanWordlists("🐟Ａb", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 4 || !out.routesTo("crisis") || !out.routesTo("school") || !out.routesTo("ordinary_violation") {
		t.Fatalf("错误的命中或流转: %+v", out)
	}
	for _, match := range out.Matches {
		if match.Start != 2 || match.End != 4 || match.Label == "upstream_disabled" {
			t.Fatalf("错误的原文位置或状态: %+v", match)
		}
	}
}

// TestWordlistTruncationAndLegacyEntries 验证截断详情不会漏掉后续危机流转，且长旧词仍有效。
func TestWordlistTruncationAndLegacyEntries(t *testing.T) {
	word := strings.Repeat("长", 300)
	out, err := scanWordlists(strings.Repeat("提示 ", wordlistMatchLimit+1)+word, rules{
		builtinEntries: []builtinRule{{Word: "提示", Disposition: hintDisposition, Enabled: true}},
		crisisEntries:  []string{word},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Matches) != wordlistMatchLimit || !out.routesTo("crisis") || out.routesTo("ordinary_violation") {
		t.Fatalf("错误的截断或流转: %+v", out)
	}
}
