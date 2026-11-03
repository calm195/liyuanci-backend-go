package gateway

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
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

// TestGatewayFailureBoundaries 验证对象服务故障、严格响应和重定向不能被当作存储成功。
func TestGatewayFailureBoundaries(t *testing.T) {
	for _, status := range []int{200, 302, 503} {
		g, cfg := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://other.invalid")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"Version":"v1","SHA256":"wrong","Secret":"禁止"}`))
		})
		if _, err := NewBusiness(g, 1024).PutOnce(context.Background(), "object", []byte{1}); err == nil {
			t.Fatal("错误对象结果被当作成功")
		}
		cfg.URL = "http://gateway.invalid"
		if _, err := New(cfg, zap.NewNop()); err == nil {
			t.Fatal("接受明文对象服务")
		}
	}
}
