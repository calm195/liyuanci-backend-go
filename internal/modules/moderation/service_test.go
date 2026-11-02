package moderation

import (
	"strings"
	"testing"
)

// TestValidation 验证 T016/REQ-015、019 的快照、独立版本及驳回双字段失败边界。
func TestValidation(t *testing.T) {
	for _, in := range []WordlistInput{{Kind: "unknown", ExpectedVersion: 1, Entries: []string{}}, {Kind: "school", ExpectedVersion: 0, Entries: []string{}}, {Kind: "school", ExpectedVersion: 1}, {Kind: "school", ExpectedVersion: 1, Entries: []string{" "}}, {Kind: "school", ExpectedVersion: 1, Entries: []string{"重复", "重复"}}} {
		if validateEntries(in, 100) == nil {
			t.Fatal("非法快照被接受")
		}
	}
	if validateEntries(WordlistInput{Kind: "school", ExpectedVersion: 1, Entries: []string{}}, 100) != nil {
		t.Fatal("明确空快照应允许")
	}
	for _, in := range []Decision{{Decision: "approved"}, {Decision: "invalid", ExpectedVersion: 1}, {Decision: "rejected", ExpectedVersion: 1, Reason: "原因"}, {Decision: "rejected", ExpectedVersion: 1, Reason: "原因", RevisionSuggestion: " "}, {Decision: "approved", ExpectedVersion: 1, Reason: strings.Repeat("x", 2001)}} {
		if validateDecision(in) == nil {
			t.Fatal("非法审核决定被接受")
		}
	}
	if validateDecision(Decision{Decision: "rejected", ExpectedVersion: 2, Reason: "原因", RevisionSuggestion: "修改后重提"}) != nil {
		t.Fatal("合法驳回应允许")
	}
}

// TestCursorIsolation 验证游标绑定管理员和队列用途，篡改不能跨范围读取。
func TestCursorIsolation(t *testing.T) {
	s := &Service{cfg: Config{CursorKey: []byte(strings.Repeat("k", 32))}}
	id := "50000000-0000-4000-8000-000000000001"
	cursor := s.cursor("actor", "cases", id)
	if got, e := s.after("actor", "cases", cursor); e != nil || got != id {
		t.Fatal("正确游标失败")
	}
	for _, in := range []struct{ actor, purpose, value string }{{"other", "cases", cursor}, {"actor", "wordlists", cursor}, {"actor", "cases", cursor + "x"}, {"actor", "cases", strings.Repeat("x", 600)}} {
		if _, e := s.after(in.actor, in.purpose, in.value); e == nil {
			t.Fatal("游标越权或篡改")
		}
	}
}
