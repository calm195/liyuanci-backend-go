package moderation

import (
	"errors"
	"testing"

	swd "github.com/kirklin/go-swd"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// TestBuiltinCatalogCoverage 验证导入制品与依赖内置词库同源，并可构建生产扫描器。
func TestBuiltinCatalogCoverage(t *testing.T) {
	catalog, err := loadBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := swd.New()
	if err != nil {
		t.Fatal(err)
	}
	upstreamWords := upstream.Words()
	snapshot := rules{builtinEntries: make([]builtinRule, 0, len(catalog.entries))}
	imported := make(map[string]bool, len(catalog.entries))
	for _, entry := range catalog.entries {
		if _, ok := upstreamWords[entry.Entry]; !ok {
			t.Fatal("词表包含上游内置词库未提供的词条")
		}
		imported[entry.Entry] = true
		snapshot.builtinEntries = append(snapshot.builtinEntries, builtinRule{
			Word: entry.Entry, Label: entry.UpstreamLabel, Disposition: entry.Action, Enabled: true,
		})
	}
	if len(imported) != len(upstreamWords) {
		t.Fatalf("导入词表数量 %d，与上游数量 %d 不一致", len(imported), len(upstreamWords))
	}
	if _, err = compileWordlists(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinOverridesPrecedence(t *testing.T) {
	enabled, disabled := true, false
	entries := []entity.WordlistEntry{
		{ID: "one", UpstreamLabel: "first", Action: reviewDisposition},
		{ID: "two", UpstreamLabel: "first", Action: reviewDisposition},
	}
	input := BuiltinWordlistInput{
		Overrides: []BuiltinWordlistOverride{{ID: "one", Enabled: &enabled}},
		LabelOverrides: []BuiltinWordlistOverride{{
			Label: "first", Enabled: &disabled,
		}},
	}
	if err := applyBuiltinOverrides(entries, input); err != nil {
		t.Fatal(err)
	}
	if entries[0].Disabled || !entries[1].Disabled {
		t.Fatal("单词覆盖应优先于同一草案的标签覆盖")
	}
}

// TestBuiltinUpstreamRefresh 验证上游移除时保留本地覆盖。
func TestBuiltinUpstreamRefresh(t *testing.T) {
	before := []entity.WordlistEntry{
		{Entry: "被移除", UpstreamLabel: "a", UpstreamAction: reviewDisposition, Action: reviewDisposition, Disabled: true},
		{Entry: "留用", UpstreamLabel: "b", UpstreamAction: hintDisposition, Action: reviewDisposition},
	}
	after := []entity.WordlistEntry{
		{Entry: "留用", UpstreamLabel: "b", UpstreamAction: reviewDisposition, Action: reviewDisposition},
		{Entry: "新增", UpstreamLabel: "c", UpstreamAction: hintDisposition, Action: hintDisposition},
	}
	var page BuiltinUpstreamChangesPage
	page.addChanges(after, before, 0, 2)
	if page.Total != 3 || page.Conflicts != 1 || page.Added != 1 || page.Removed != 1 ||
		page.DefaultChanged != 1 || len(page.Items) != 2 || !page.Items[0].Conflict {
		t.Fatalf("上游差异或冲突分页不符：%+v", page)
	}
	if _, err := carryBuiltinOverrides(after, before); !errors.Is(err, command.ErrConflict) {
		t.Fatalf("上游更新需保护待移除的本地覆盖：%v", err)
	}
	before[0].Disabled = false
	before[1].Action = hintDisposition
	result, err := carryBuiltinOverrides(after, before)
	if err != nil || len(result) != 2 || result[0].Action != reviewDisposition {
		t.Fatalf("没有本地覆盖时应采用上游默认动作：%+v %v", result, err)
	}
}
