package wish

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"go.uber.org/zap"
)

// TestTitleAndCustomRewardBoundary 防止新增标题及自由报答绕过字段校验。
func TestTitleAndCustomRewardBoundary(t *testing.T) {
	policy, err := NewPolicy(zap.NewNop(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, title, reward, method string
		valid                       bool
	}{
		{"旧客户端缺标题", "", "", "sincere_thanks", true},
		{"标题和自定义报答", "一起学习", "请喝咖啡", "custom", true},
		{"标题过长", strings.Repeat("题", 41), "", "sincere_thanks", false},
		{"标题全空白", " ", "", "sincere_thanks", false},
		{"自定义报答缺失", "标题", "", "custom", false},
		{"报答过长", "标题", strings.Repeat("谢", 101), "custom", false},
		{"预选报答混入正文", "标题", "额外文本", "meal", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			out, err := policy.ValidateContent(context.Background(), "", ContentInput{Title: tc.title, RewardText: tc.reward, RewardMethod: &method, Body: "正文", Category: "help", Campus: "any"})
			if tc.valid {
				if err != nil || out.Title == "" {
					t.Fatal(out, err)
				}
			} else if !errors.Is(err, command.ErrInvalid) {
				t.Fatal(out, err)
			}
		})
	}
}
