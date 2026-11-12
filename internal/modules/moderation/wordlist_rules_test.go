package moderation

import (
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// TestCustomWordlistAliasesAndBoundaries 验证显式拼音缩写、英文边界及只提示不会进入普通人工审核。
func TestCustomWordlistAliasesAndBoundaries(t *testing.T) {
	rule := WordlistRule{Entry: "网络词", Category: "abbreviation", Aliases: []string{"ncy"}, MatchMode: "word", Action: "review"}
	in := WordlistInput{Kind: "ordinary_violation", Entries: []string{"网络词"}, Rules: []WordlistRule{rule}}
	if e := validateWordlistRules(in); e != nil {
		t.Fatal(e)
	}
	snapshot := rules{ordinaryEntries: in.Entries, customRules: map[string][]WordlistRule{"ordinary_violation": in.Rules}}
	matcher, e := compileWordlists(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		text string
		hit  bool
	}{{"ncy", true}, {"这 ncy 表达", true}, {"fancynoun", false}, {"xncy", false}, {"ncy123", false}, {"网络词", true}} {
		got := matcher.scan(tc.text)
		if got.routesTo("ordinary_violation") != tc.hit {
			t.Fatalf("%s: %+v", tc.text, got)
		}
	}
	rule.Action = "hint"
	snapshot.customRules["ordinary_violation"] = []WordlistRule{rule}
	matcher, e = compileWordlists(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	got := matcher.scan("ncy")
	if len(got.Matches) == 0 || got.routesTo("ordinary_violation") {
		t.Fatal("hint路由不正确", got)
	}
	for _, kind := range []string{"school", "crisis"} {
		in.Kind = kind
		in.Rules = []WordlistRule{rule}
		if validateWordlistRules(in) == nil {
			t.Fatal("硬路由允许hint", kind)
		}
	}
	legacy := ruleFromEntry(entity.WordlistEntry{Entry: "历史词", Action: "isolate"})
	if legacy.Action != "review" || legacy.MatchMode != "contains" {
		t.Fatal("历史审核语义改变")
	}
}
