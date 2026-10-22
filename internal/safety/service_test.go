package safety

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// TestWarningBoundary 覆盖未展示、向上取整、二十九秒和三十秒服务器边界。
func TestWarningBoundary(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Second)
	p := entity.Penalty{ID: uuid.NewString(), Version: 1}
	if warning(p, start).RemainingSeconds != 30 {
		t.Fatal("未展示必须三十秒")
	}
	p.FirstDisplayedAt = &start
	p.ConfirmableAt = &end
	for _, tc := range []struct {
		elapsed time.Duration
		want    int
	}{{0, 30}, {time.Millisecond, 30}, {29 * time.Second, 1}, {30 * time.Second, 0}, {31 * time.Second, 0}} {
		if confirmable(p, start.Add(tc.elapsed)) != (tc.elapsed >= 30*time.Second) {
			t.Fatal("警告确认边界错误")
		}
		if got := warning(p, start.Add(tc.elapsed)); got.RemainingSeconds != tc.want {
			t.Fatalf("%v: %d want %d", tc.elapsed, got.RemainingSeconds, tc.want)
		}
	}
}

// TestInputValidation 拒绝任意时长之外的非法等级、缺少理由、旧版及非本人目标格式。
func TestInputValidation(t *testing.T) {
	s := &Service{cfg: Config{MaxTextBytes: 100}}
	id := uuid.NewString()
	for _, tc := range []struct {
		op, id string
		in     Input
		valid  bool
	}{
		{"block", "", Input{TargetUserID: id}, true},
		{"block", "", Input{}, false},
		{"report", "", Input{TargetKind: "account", ID: id, TypeCode: "synthetic"}, false},
		{"report", "", Input{TargetKind: "wish", ID: id, TypeCode: "synthetic"}, true},
		{"appeal", "", Input{PenaltyID: id, Body: " "}, false},
		{"appeal", "", Input{PenaltyID: id, Body: strings.Repeat("a", 101)}, false},
		{"ack", id, Input{ExpectedVersion: 0}, false},
		{"report_decision", id, Input{Decision: "confirmed", ExpectedVersion: 1, Reason: "合成"}, true},
		{"appeal_decision", id, Input{Decision: "revoke", ExpectedVersion: 1}, false},
		{"penalty", id, Input{ExpectedVersion: 1, ViolationRef: id, ContentRef: id, ContentVersion: 1, Level: "custom", Reason: "合成"}, false},
		{"unknown", id, Input{}, false},
	} {
		e := s.validate(tc.op, tc.id, tc.in)
		if (e == nil) != tc.valid {
			t.Errorf("%s: %v", tc.op, e)
		}
	}
}

// TestCursorScope 验证游标不能跨主体或用途重放，篡改和过长输入失败关闭。
func TestCursorScope(t *testing.T) {
	s := &Service{cfg: Config{Key: []byte(strings.Repeat("k", 32))}}
	at := time.Now().UTC()
	id := uuid.NewString()
	c := s.cursor("actor", "reports", at, id)
	got, rid, e := s.after("actor", "reports", c)
	if e != nil || !got.Equal(at) || rid != id {
		t.Fatal("游标往返失败")
	}
	for _, args := range [][3]string{{"other", "reports", c}, {"actor", "appeals", c}, {"actor", "reports", c + "x"}, {"actor", "reports", strings.Repeat("x", 1025)}} {
		if _, _, e := s.after(args[0], args[1], args[2]); e == nil {
			t.Fatal("错误游标通过")
		}
	}
}
