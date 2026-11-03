package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/config"
)

func testBusinessSettings(t *testing.T) config.Business {
	t.Helper()
	c := config.Business{
		Runtime:         testSettings(),
		LookupKeyRefs:   []string{"lookup-v1"},
		WeChatAppID:     "synthetic-app",
		SessionTTL:      time.Hour,
		ExchangeTTL:     time.Minute,
		AdminSessionTTL: time.Hour,
		LoginWindow:     time.Minute,
		LoginLimit:      30,
		MaxTextBytes:    65536,
	}
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
		{
			"收紧连接池",
			func(c *config.Business) { c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 2, 1 },
			true,
		},
		{
			"池配置不完整",
			func(c *config.Business) { c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 0, 1 },
			false,
		},
		{
			"池空闲超过总额",
			func(c *config.Business) { c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 1, 2 },
			false,
		},
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
