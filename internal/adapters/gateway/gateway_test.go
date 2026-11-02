package gateway

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/privacy"
)

func testGateway(t *testing.T, handler http.HandlerFunc) (*Client, Config) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cfg := Config{Version: "test-v1", Timeout: time.Second}
	cfg.URL = server.URL
	cfg.Token = "synthetic-gateway-token"
	cfg.CA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	g, e := New(cfg, zap.NewNop())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(g.Close)
	return g, cfg
}

// TestGatewayFailureBoundaries 验证私密分类不能返回替换正文、清理确认须含写屏障、TLS重定向不得携凭据外发。
func TestGatewayFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		call       func(*Client) error
	}{
		{"私密正文替换拒绝", `{"Body":"不允许替换","DetectorVersion":"test"}`, 200, func(g *Client) error {
			_, e := g.ClassifyWhisperCapsule(context.Background(), "合成私密正文")
			return e
		}},
		{"无清理写屏障", `{"Completed":true,"WriteBarrierConfirmed":false}`, 200, func(g *Client) error { return g.Erase(context.Background(), privacy.Intent{}, nil) }},
		{"通知未确认", `{"Completed":false}`, 200, func(g *Client) error { return g.NotifyCrisis(context.Background(), "delivery", "case") }},
		{"供应商不可用", `{"secret":"不得透传"}`, 503, func(g *Client) error { return g.Ready(context.Background()) }},
		{"健康版本不符", `{"Version":"other","Capabilities":[]}`, 200, func(g *Client) error { return g.Ready(context.Background()) }},
		{"重定向拒绝", ``, 302, func(g *Client) error { return g.Ready(context.Background()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://other.invalid")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			if e := tc.call(g); e == nil {
				t.Fatal("依赖故障未关闭")
			}
		})
	}
}

// TestKeyReferenceIsolation 未授权历史引用不得发送给供应商，恢复用途与业务用途不互换。
func TestKeyReferenceIsolation(t *testing.T) {
	calls := 0
	g, cfg := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"Key": make([]byte, 32)})
	})
	k := Keys{g, []string{"business-v1"}, "business"}
	if _, e := k.Unwrap(context.Background(), "journal-v1", []byte{1}); e == nil || calls != 0 {
		t.Fatal("跨用途引用到达供应商")
	}
	cfg.URL = "http://gateway.invalid"
	if _, e := New(cfg, zap.NewNop()); e == nil {
		t.Fatal("接受明文供应商")
	}
}
