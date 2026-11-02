package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestBusinessGatewayRejectsAmbiguousResults 缺少显式扫描结论、错误不可变对象版本和不完整能力声明必须失败。
func TestBusinessGatewayRejectsAmbiguousResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		call       func(*Business) error
	}{
		{"扫描无结论", `{"DetectorVersion":"v1"}`, func(g *Business) error { _, e := g.Scan(context.Background(), []byte{1}, "image/png"); return e }},
		{"学校无结论", `{"RulesVersion":"v1"}`, func(g *Business) error { return g.Validate(context.Background(), "synthetic", "合成") }},
		{"对象版本错误", `{"Version":"wrong","Data":"AQ=="}`, func(g *Business) error { _, e := g.Open(context.Background(), "object", "expected"); return e }},
		{"对象摘要不一致", `{"Version":"v1","SHA256":"wrong"}`, func(g *Business) error { _, e := g.PutOnce(context.Background(), "object", []byte{1}); return e }},
		{"内容返回私密字段", `{"Body":"合成","DetectorVersion":"v1","Secret":"禁止"}`, func(g *Business) error { _, e := g.AnalyzeMessage(context.Background(), "合成"); return e }},
		{"缺业务能力", `{"Version":"test-v1","Capabilities":["wrap","unwrap","redact","erase"]}`, func(g *Business) error { return g.ReadyFor(context.Background(), "api") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			b := &Business{Client: g, maxObjectBytes: 1024}
			if tc.call(b) == nil {
				t.Fatal("无效供应商响应被接受")
			}
		})
	}
	called := false
	g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"Digest": make([]byte, 32)})
	})
	b := &Business{Client: g, lookupRefs: []string{"lookup-v1"}}
	if _, e := b.Digest(context.Background(), "business-v1", "openid", []byte("synthetic")); e == nil || called {
		t.Fatal("未授权检索引用到达供应商")
	}
}
