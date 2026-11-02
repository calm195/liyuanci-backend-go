package whisperapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/config"
)

func testBusinessSettings(t *testing.T) config.Business {
	t.Helper()
	c := config.Business{Runtime: testSettings(), LookupKeyRefs: []string{"lookup-v1"}, WeChatAppID: "synthetic-app", SessionTTL: time.Hour, ExchangeTTL: time.Minute, AdminSessionTTL: time.Hour, LoginWindow: time.Minute, LoginLimit: 30, MaxTextBytes: 65536}
	for i, k := range []*[]byte{&c.SessionKey, &c.CardKey, &c.VerificationKey, &c.WishKey, &c.AdminKey, &c.CrisisKey} {
		*k = bytes.Repeat([]byte{byte(30 + i)}, 32)
	}
	c.WeChatSecret = "synthetic-wechat-secret"
	c.AdminPassword = "synthetic-business-secret"
	c.Card.MaxBytes, c.Card.MaxPixels, c.Card.DecodeSlots, c.Card.Lease, c.Card.RetryDelay, c.Card.MaxAttempts = 1<<20, 4_000_000, 2, 30*time.Second, time.Second, 3
	return c
}

// TestBusinessConfigurationFailClosed 验证生产启用只走完整宿主，不能混入本地模拟、轮询或跨用途凭据。
func TestBusinessConfigurationFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.Business)
		ok     bool
	}{
		{"完整生产含悄悄鲤", func(c *config.Business) {}, true},
		{"收紧连接池", func(c *config.Business) { c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 2, 1 }, true},
		{"池配置不完整", func(c *config.Business) { c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 0, 1 }, false},
		{"池空闲超过总额", func(c *config.Business) { c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 1, 2 }, false},
		{"池预算扩张", func(c *config.Business) { c.Runtime.DatabaseMaxOpen = 101 }, false},
		{"本地适配", func(c *config.Business) { c.Runtime.LocalTest = true }, false},
		{"明文供应商", func(c *config.Business) { c.Runtime.GatewayURL = "http://gateway.invalid" }, false},
		{"缺微信凭据", func(c *config.Business) { c.WeChatSecret = "" }, false},
		{"检索与包裹混用", func(c *config.Business) { c.LookupKeyRefs = c.Runtime.KeyRefs }, false},
		{"缺身份池", func(c *config.Business) { c.Runtime.DatabaseDSN = "" }, false},
		{"空上传预算", func(c *config.Business) { c.Card.MaxBytes = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testBusinessSettings(t)
			tc.change(&c)
			p := filepath.Join(t.TempDir(), "business.yaml")
			_ = os.WriteFile(filepath.Join(filepath.Dir(p), "common.yaml"), []byte("mode: foundation\n"), 0600)
			if e := os.WriteFile(p, mustYAML(t, businessDocument(c)), 0600); e != nil {
				t.Fatal(e)
			}
			_, e := config.LoadBusiness(context.Background(), p, "production", "api")
			if (e == nil) != tc.ok {
				t.Fatal("配置准入不符", e)
			}
		})
	}
	c := testBusinessSettings(t)
	c.CardKey = c.Runtime.ReceiptKey
	if config.ValidateBusiness(c, "production", "api") == nil {
		t.Fatal("领域构造器接受跨用途共钥")
	}
	if _, e := config.LoadWhispers(context.Background(), writeSettings(t, c.Runtime), "production"); e == nil {
		t.Fatal("独立旧入口绕过完整宿主门禁")
	}

}

// TestBusinessGatewayRejectsAmbiguousResults 缺少显式扫描结论、错误不可变对象版本和不完整能力声明必须失败。
func TestBusinessGatewayRejectsAmbiguousResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		call       func(*businessGateway) error
	}{
		{"扫描无结论", `{"DetectorVersion":"v1"}`, func(g *businessGateway) error { _, e := g.Scan(context.Background(), []byte{1}, "image/png"); return e }},
		{"学校无结论", `{"RulesVersion":"v1"}`, func(g *businessGateway) error { return g.Validate(context.Background(), "synthetic", "合成") }},
		{"对象版本错误", `{"Version":"wrong","Data":"AQ=="}`, func(g *businessGateway) error { _, e := g.Open(context.Background(), "object", "expected"); return e }},
		{"对象摘要不一致", `{"Version":"v1","SHA256":"wrong"}`, func(g *businessGateway) error { _, e := g.PutOnce(context.Background(), "object", []byte{1}); return e }},
		{"内容返回私密字段", `{"Body":"合成","DetectorVersion":"v1","Secret":"禁止"}`, func(g *businessGateway) error { _, e := g.AnalyzeMessage(context.Background(), "合成"); return e }},
		{"缺业务能力", `{"Version":"test-v1","Capabilities":["wrap","unwrap","redact","erase"]}`, func(g *businessGateway) error { return g.readyBusiness(context.Background(), "api") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			b := &businessGateway{gateway: g, maxObjectBytes: 1024}
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
	b := &businessGateway{gateway: g, lookupRefs: []string{"lookup-v1"}}
	if _, e := b.Digest(context.Background(), "business-v1", "openid", []byte("synthetic")); e == nil || called {
		t.Fatal("未授权检索引用到达供应商")
	}
}
