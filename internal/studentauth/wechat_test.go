package studentauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// T008/REQ-002、018：固定微信code2Session协议适配；Transport为合成夹具，不发真实微信请求。
func TestWeChatExchange(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		status             int
		failure, errorWant error
	}{
		{name: "success", status: 200, body: `{"openid":"synthetic-openid","session_key":"SYNTHETIC_SESSION_KEY"}`},
		{name: "expired_code", status: 200, body: `{"errcode":40163,"errmsg":"SYNTHETIC_SECRET"}`, errorWant: command.ErrUnauthenticated},
		{name: "invalid_code", status: 200, body: `{"errcode":40029}`, errorWant: command.ErrUnauthenticated},
		{name: "provider_failure", status: 200, body: `{"errcode":-1,"errmsg":"SYNTHETIC_SECRET"}`, errorWant: command.ErrUnavailable},
		{name: "missing_identity", status: 200, body: `{"session_key":"SYNTHETIC_SESSION_KEY"}`, errorWant: command.ErrUnavailable},
		{name: "duplicate", status: 200, body: `{"openid":"a","openid":"b"}`, errorWant: command.ErrUnavailable},
		{name: "trailing", status: 200, body: `{"openid":"a"}{}`, errorWant: command.ErrUnavailable},
		{name: "too_large", status: 200, body: strings.Repeat("x", 32769), errorWant: command.ErrUnavailable},
		{name: "redirect", status: 302, body: "", errorWant: command.ErrUnavailable},
		{name: "transport", failure: errors.New("SYNTHETIC_SECRET"), errorWant: command.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			client := &http.Client{Timeout: time.Second, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" || r.URL.Host != "api.weixin.qq.com" || r.URL.Path != "/sns/jscode2session" || r.URL.Query().Get("grant_type") != "authorization_code" || r.URL.Query().Get("js_code") != "synthetic-code" || r.URL.Query().Get("appid") != "synthetic-app" || r.URL.Query().Get("secret") != "SYNTHETIC_SECRET" {
					t.Fatal("微信请求协议不符")
				}
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}, Request: r}, nil
			})}
			adapter, e := NewWeChat(client, "synthetic-app", "SYNTHETIC_SECRET", zap.New(core))
			if e != nil {
				t.Fatal(e)
			}
			identity, e := adapter.Exchange(context.Background(), "synthetic-code")
			if e != tc.errorWant {
				t.Fatal(e)
			}
			if e == nil && string(identity) != "synthetic-openid" {
				t.Fatal("身份返回错误")
			}
			entries := logs.FilterMessage("微信小程序身份交换完成").All()
			if len(entries) != 1 {
				t.Fatal("缺完成日志")
			}
			for _, entry := range entries {
				for _, v := range entry.ContextMap() {
					if strings.Contains(strings.ToLower(strings.TrimSpace(toString(v))), "synthetic") {
						t.Fatal("日志含敏感数据")
					}
				}
			}
		})
	}
}

func toString(v any) string { value, _ := v.(string); return value }
