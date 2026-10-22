package wish

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// TestApplicationMessageCodePoints 保留REQ-006对Unicode码点和空留言的正式契约边界。
func TestApplicationMessageCodePoints(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{{"空留言", "", true}, {"一百汉字", strings.Repeat("鲤", 100), true}, {"一百零一汉字", strings.Repeat("鲤", 101), false}, {"一百emoji", strings.Repeat("🐟", 100), true}, {"非法UTF8", string([]byte{0xff}), false}} {
		t.Run(tc.name, func(t *testing.T) {
			if validApplicationMessage(tc.value) != tc.valid {
				t.Fatal("码点边界错误")
			}
		})
	}
}

// TestInteractionInputBeforeIO 验证无效输入在命令引擎或供应商访问前拒绝，日志依赖仍显式注入。
func TestInteractionInputBeforeIO(t *testing.T) {
	s := &Service{policy: &Policy{logger: zap.NewNop()}}
	id := "30000000-0000-4000-8000-000000000001"
	if _, e := s.Apply(context.Background(), "token", "key", "test", id, ApplicationInput{Message: strings.Repeat("x", 101), WishContentVersion: 1}); e != command.ErrInvalid {
		t.Fatal(e)
	}
	if _, e := s.AcceptApplication(context.Background(), "token", "key", "test", id, AcceptInput{}); e != command.ErrInvalid {
		t.Fatal(e)
	}
	if _, e := s.WithdrawApplication(context.Background(), "token", "key", "test", id, 0); e != command.ErrInvalid {
		t.Fatal(e)
	}
	if _, e := s.Rollback(context.Background(), "token", "key", "test", id, RollbackInput{ExpectedVersion: 1, Target: Matching, RefreshExpiry: true}); e != command.ErrInvalid {
		t.Fatal(e)
	}
	if _, e := s.OwnApplications(context.Background(), "token", "test", ApplicationQuery{Limit: 20, Status: "active|accepted"}); e != command.ErrInvalid {
		t.Fatal(e)
	}
}
