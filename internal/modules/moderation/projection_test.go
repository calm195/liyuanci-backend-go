package moderation

import (
	"encoding/json"
	"testing"
)

// TestGovernancePageProtocol 固定空扫描仍有游标及末页的JSON语义，避免投影归属调整改变客户端翻页。
func TestGovernancePageProtocol(t *testing.T) {
	cursor := "opaque-cursor"
	for _, tc := range []struct {
		name  string
		value CursorPage[string]
		want  string
	}{
		{
			"空扫描继续",
			CursorPage[string]{Items: []string{}, NextCursor: &cursor},
			`{"items":[],"next_cursor":"opaque-cursor"}`,
		},
		{"末页", CursorPage[string]{Items: []string{"result"}}, `{"items":["result"],"next_cursor":null}`},
		{"零值", CursorPage[string]{}, `{"items":null,"next_cursor":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			if err != nil || string(data) != tc.want {
				t.Fatalf("分页协议变化：%s %v", data, err)
			}
		})
	}
}
