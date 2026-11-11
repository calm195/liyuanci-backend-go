package moderation

import (
	"strings"
	"testing"
)

// TestCaseQueryValidation 验证筛选默认值与不允许扩大的状态/内容范围。
func TestCaseQueryValidation(t *testing.T) {
	got, err := normalizeCaseQuery(CaseQuery{Q: " abc "})
	if err != nil || got.Status != "human_required" || got.Limit != 20 || got.Q != "abc" {
		t.Fatalf("默认查询错误：%+v %v", got, err)
	}
	for _, q := range []CaseQuery{{Q: strings.Repeat("长", 81)}, {Q: "\x00"}, {Status: "pending"}, {Kind: "whisper_capsule"}, {Limit: 101}, {Limit: -1}} {
		if _, err := normalizeCaseQuery(q); err == nil {
			t.Fatalf("允许非法筛选：%+v", q)
		}
	}
}

// TestCaseCursorBindings 验证主体、筛选及排序分组均受签名约束。
func TestCaseCursorBindings(t *testing.T) {
	s := &Service{cfg: Config{CursorKey: []byte(strings.Repeat("k", 32))}}
	actor := "00000000-0000-4000-8000-000000000001"
	id := "00000000-0000-4000-8000-000000000002"
	q := CaseQuery{Q: "abc", Status: "all", Kind: "wish", Limit: 20}
	q.Cursor = s.cursor(actor, s.casePurpose(q)+":1", id)
	got, rank, err := s.caseAfter(actor, q)
	if err != nil || got != id || rank != 1 {
		t.Fatalf("游标读取错误：%s %d %v", got, rank, err)
	}
	for _, changed := range []CaseQuery{{Q: "changed", Status: q.Status, Kind: q.Kind, Cursor: q.Cursor}, {Q: q.Q, Status: "human_required", Kind: q.Kind, Cursor: q.Cursor}, {Q: q.Q, Status: q.Status, Kind: "message", Cursor: q.Cursor}} {
		if _, _, err := s.caseAfter(actor, changed); err == nil {
			t.Fatal("游标跨筛选复用")
		}
	}
	if _, _, err := s.caseAfter("other-admin", q); err == nil {
		t.Fatal("游标跨账号复用")
	}
}
