package moderation

import (
	"testing"
	"time"
)

// TestCrisisHandoff 验证REQ-017北京时间8/22时边界和跨日自动待办窗口。
func TestCrisisHandoff(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2026-10-23T07:59:59+08:00", "2026-10-23T08:00:00+08:00"},
		{"2026-10-23T08:00:00+08:00", "2026-10-23T08:00:00+08:00"},
		{"2026-10-23T21:59:59+08:00", "2026-10-23T21:59:59+08:00"},
		{"2026-10-23T22:00:00+08:00", "2026-10-24T08:00:00+08:00"},
		{"2026-12-31T23:00:00+08:00", "2027-01-01T08:00:00+08:00"},
	} {
		in, _ := time.Parse(time.RFC3339, tc.in)
		want, _ := time.Parse(time.RFC3339, tc.want)
		if !HandoffAt(in).Equal(want) {
			t.Fatal(tc.in, HandoffAt(in))
		}
	}
}
