package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func TestWeChatMiniappCode(t *testing.T) {
	const scene = "0123456789abcdef0123456789abcdef"
	var jpegCode bytes.Buffer
	if err := jpeg.Encode(&jpegCode, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	var requests atomic.Int32
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Scheme != "https" || r.URL.Host != "api.weixin.qq.com" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Error("微信请求协议错误")
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			return nil, err
		}
		var body []byte
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			if input["appid"] != "synthetic-app" || input["secret"] != "synthetic-secret" ||
				input["grant_type"] != "client_credential" || r.URL.RawQuery != "" {
				t.Error("微信凭据请求字段错误")
			}
			sequence := refreshes.Add(1)
			body = fmt.Appendf(nil, `{"access_token":"synthetic-token-%d","expires_in":7200}`, sequence)
		case "/wxa/getwxacodeunlimit":
			requests.Add(1)
			if input["page"] != "pages/stories/index" || input["scene"] != scene || input["env_version"] != "trial" || input["check_path"] != false {
				t.Error("微信码请求字段错误")
			}
			switch r.URL.Query().Get("access_token") {
			case "synthetic-token-1":
				body = []byte(`{"errcode":42001}`)
			case "synthetic-token-2":
				body = jpegCode.Bytes()
			default:
				t.Error("未使用预期凭据")
			}
		default:
			t.Error("请求了未预期的微信接口")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})
	w, err := NewWeChat(&http.Client{Timeout: time.Second, Transport: transport}, "synthetic-app", "synthetic-secret", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.MiniappCode(t.Context(), "pages/invalid/index", scene, "trial"); !errors.Is(err, command.ErrInvalid) {
		t.Fatal("非法页面未拒绝", err)
	}
	var workers sync.WaitGroup
	for range 6 {
		workers.Go(func() {
			code, err := w.MiniappCode(t.Context(), "pages/stories/index", scene, "trial")
			if err != nil || http.DetectContentType(code) != "image/png" {
				t.Error("未返回规范化 PNG", err)
			}
		})
	}
	workers.Wait()
	if got := refreshes.Load(); got != 2 {
		t.Fatalf("并发调用应合并凭据刷新，实际请求 %d 次", got)
	}
	if got := requests.Load(); got < 7 || got > 12 {
		t.Fatalf("每个请求最多重试一次，实际请求 %d 次", got)
	}
}
