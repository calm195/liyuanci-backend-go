package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestWeChatStartImageCheck 检查 v2 请求字段、接口地址和异步回执。
func TestWeChatStartImageCheck(t *testing.T) {
	const imageURL = "https://synthetic.invalid/api/v1/wechat/media/source?id=fixture"
	client := &http.Client{
		Timeout: time.Second,
		Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			var value struct {
				OpenID    string `json:"openid"`
				URL       string `json:"media_url"`
				MediaType int    `json:"media_type"`
				Scene     int    `json:"scene"`
				Version   int    `json:"version"`
			}
			if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
				t.Fatal(err)
			}
			if r.Method != http.MethodPost || r.URL.Host != "api.weixin.qq.com" || r.URL.Path != "/wxa/media_check_async" ||
				r.URL.Query().Get("access_token") != "synthetic-token" || value.OpenID != "synthetic-openid" ||
				value.Version != 2 || value.MediaType != 2 || value.Scene != 1 || value.URL != imageURL {
				t.Fatal("微信图片审核请求不符合约定")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errcode":0,"trace_id":"synthetic-trace"}`))}, nil
		}),
	}
	w, err := NewWeChat(client, "synthetic-app", "synthetic-secret", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	w.codeTokens.value, w.codeTokens.expiresAt = "synthetic-token", time.Now().Add(time.Minute)
	if trace, err := w.StartImageCheck(context.Background(), "synthetic-openid", 1, imageURL); err != nil || trace != "synthetic-trace" {
		t.Fatalf("微信图片审核回执不正确：%q, %v", trace, err)
	}
}
