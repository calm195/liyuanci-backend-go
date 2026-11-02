package whisper

import (
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// TestCapsuleTimeBoundary 验证以数据库整秒时刻为基准的含边界窗口，不使用宿主时钟。
func TestCapsuleTimeBoundary(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 900000000, time.UTC)
	base := now.Truncate(time.Second)
	for _, tc := range []struct {
		name  string
		at    time.Time
		valid bool
	}{
		{"提前59秒", base.Add(59 * time.Second), false}, {"恰好60秒", base.Add(60 * time.Second), true},
		{"恰好365天", base.Add(365 * 24 * time.Hour), true}, {"多一秒", base.Add(365*24*time.Hour + time.Second), false},
		{"非整秒", base.Add(61*time.Second + time.Nanosecond), false}, {"非UTC", base.Add(time.Minute).In(time.FixedZone("测试时区", 8*3600)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validCapsuleTime(now, tc.at); got != tc.valid {
				t.Fatal(got)
			}
		})
	}
}

// TestCapsuleReadBoundary 验证时间推导状态不改持久版本，终态和屏障优先。
func TestCapsuleReadBoundary(t *testing.T) {
	at := time.Date(2026, 10, 28, 0, 1, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, review, lifecycle string
		flags                   capsuleFlags
		now                     time.Time
		want                    string
	}{
		{"前一秒", "approved", "active", capsuleFlags{}, at.Add(-time.Second), "locked"},
		{"到点", "approved", "active", capsuleFlags{}, at, "unlocked"},
		{"到期仍待审", "pending", "active", capsuleFlags{}, at, "review_pending"},
		{"拒绝不自动解锁", "rejected", "active", capsuleFlags{}, at, "rejected"},
		{"隐私优先", "approved", "active", capsuleFlags{Privacy: true}, at, "isolated"},
		{"治理优先", "approved", "active", capsuleFlags{Restricted: true}, at, "isolated"},
		{"删除优先", "approved", "deleted", capsuleFlags{Restricted: true}, at, "deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := entity.WhisperCapsule{Version: 7, ReviewState: tc.review, Lifecycle: tc.lifecycle, UnlockAt: at}
			if got := capsuleStatus(c, tc.flags, tc.now); got != tc.want || c.Version != 7 {
				t.Fatal(got, c.Version)
			}
		})
	}
}
