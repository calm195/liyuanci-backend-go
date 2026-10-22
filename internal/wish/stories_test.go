package wish

import (
	"strings"
	"testing"
)

// TestStoryBodyBudget 验证无产品字符限制，空白、非法编码及部署字节超限拒绝。
func TestStoryBodyBudget(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		budget     int
		want       bool
	}{{"超过心愿字符数", strings.Repeat("鲤", 501), 65536, true}, {"技术上界", "abcd", 4, true}, {"技术超界", "abcde", 4, false}, {"空正文", "", 65536, false}, {"Unicode空白", " \n\u3000", 65536, false}, {"非法编码", string([]byte{255}), 65536, false}, {"未显式启用", "正文", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			if validStoryBody(tc.body, tc.budget) != tc.want {
				t.Fatal("正文校验不符")
			}
		})
	}
}
