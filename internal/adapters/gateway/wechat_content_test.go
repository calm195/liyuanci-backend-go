package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func TestWeChatPublicText(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		want           error
	}{
		{"通过", `{"errcode":0,"result":{"suggest":"pass"}}`, nil},
		{"复核", `{"errcode":0,"result":{"suggest":"review"}}`, command.ErrContentReview},
		{"违规", `{"errcode":0,"result":{"suggest":"risky","label":20001}}`, command.ErrContentViolation},
		{"违规错误码", `{"errcode":87014}`, command.ErrContentViolation},
		{"缺失建议", `{"errcode":0,"result":{}}`, command.ErrUnavailable},
		{"空错误码", `{"errcode":null,"result":{"suggest":"pass"}}`, command.ErrUnavailable},
		{"限流", `{"errcode":45009,"result":{"suggest":"pass"}}`, command.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Timeout: time.Second, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				var body struct {
					OpenID  string `json:"openid"`
					Content string `json:"content"`
					Version int    `json:"version"`
					Scene   int    `json:"scene"`
				}
				err := json.NewDecoder(r.Body).Decode(&body)
				if err != nil || body.OpenID != "synthetic-openid" || body.Content != "评论测试" || body.Version != 2 || body.Scene != 2 ||
					r.Method != http.MethodPost || r.URL.Path != "/wxa/msg_sec_check" || r.URL.Query().Get("access_token") != "synthetic-token" {
					t.Fatal("微信评论检测请求不符合约定")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})}
			wechat, err := NewWeChat(client, "fixture", "fixture", zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			wechat.codeTokens.value = "synthetic-token"
			wechat.codeTokens.expiresAt = time.Now().Add(time.Minute)
			if err = wechat.CheckPublicText(context.Background(), "synthetic-openid", 2, "评论测试"); !errors.Is(err, tc.want) {
				t.Fatalf("所得错误 %v，期望 %v", err, tc.want)
			}
		})
	}
}
