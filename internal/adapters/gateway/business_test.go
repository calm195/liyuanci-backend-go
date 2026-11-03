package gateway

import (
	"context"
	"net/http"
	"testing"
)

// TestBusinessGatewayRejectsAmbiguousResults 缺少摘要或不可变对象版本不一致必须失败。
func TestBusinessGatewayRejectsAmbiguousResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		call       func(*Business) error
	}{
		{
			"对象版本错误",
			`{"Version":"wrong","Data":"AQ=="}`,
			func(g *Business) error {

				_, e := g.Open(context.Background(), "object", "expected")
				return e
			},
		},
		{
			"对象摘要不一致",
			`{"Version":"v1","SHA256":"wrong"}`,
			func(g *Business) error {

				_, e := g.PutOnce(context.Background(), "object", []byte{1})
				return e
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			b := &Business{Client: g, maxObjectBytes: 1024}
			if tc.call(b) == nil {
				t.Fatal("无效供应商响应被接受")
			}
		})
	}
}
