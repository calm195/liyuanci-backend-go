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
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

func testBusinessSettings(t *testing.T) config.Business {
	t.Helper()
	c := config.Business{Runtime: testSettings(), StudentDSN: "identity", AdminDSN: "admin", LookupKeyRefs: []string{"lookup-v1"}, WeChatAppID: "synthetic-app", SessionTTL: time.Hour, ExchangeTTL: time.Minute, AdminSessionTTL: time.Hour, LoginWindow: time.Minute, LoginLimit: 30, MaxTextBytes: 65536}
	for i, k := range []*[]byte{&c.SessionKey, &c.CardKey, &c.VerificationKey, &c.WishKey, &c.AdminKey, &c.CrisisKey} {
		*k = bytes.Repeat([]byte{byte(30 + i)}, 32)
	}
	c.WeChatSecretFile = filepath.Join(t.TempDir(), "wechat.secret")
	c.AdminPasswordFile = filepath.Join(t.TempDir(), "admin.password")
	for _, p := range []string{c.WeChatSecretFile, c.AdminPasswordFile} {
		if e := os.WriteFile(p, []byte("synthetic-business-secret"), 0600); e != nil {
			t.Fatal(e)
		}
	}
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
		{"池配置不完整", func(c *config.Business) { c.Runtime.DatabaseMaxIdle = 1 }, false},
		{"池空闲超过总额", func(c *config.Business) { c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle = 1, 2 }, false},
		{"池预算扩张", func(c *config.Business) { c.Runtime.DatabaseMaxOpen = 11 }, false},
		{"本地适配", func(c *config.Business) { c.Runtime.LocalTest = true }, false},
		{"无MQ轮询", func(c *config.Business) { c.Runtime.DatabasePolling = true }, false},
		{"正式数据库任务", func(c *config.Business) { c.Runtime.TaskTransport = "postgres" }, true},
		{"未知任务通道", func(c *config.Business) { c.Runtime.TaskTransport = "memory" }, false},
		{"混入开发轮询", func(c *config.Business) { c.Runtime.TaskTransport = "postgres"; c.Runtime.DatabasePolling = true }, false},
		{"数据库任务混入MQ", func(c *config.Business) { c.Runtime.TaskTransport = "postgres"; c.CardMQFile = "unused" }, false},
		{"数据库任务混入树洞MQ", func(c *config.Business) { c.Runtime.TaskTransport = "postgres"; c.Runtime.ReviewMQFile = "unused" }, false},
		{"明文供应商", func(c *config.Business) { c.Runtime.GatewayURL = "http://gateway.invalid" }, false},
		{"缺微信凭据", func(c *config.Business) { c.WeChatSecretFile = "" }, false},
		{"复用业务密钥", func(c *config.Business) { c.CardKey = c.Runtime.ReceiptKey }, false},
		{"检索与包裹混用", func(c *config.Business) { c.LookupKeyRefs = c.Runtime.KeyRefs }, false},
		{"缺身份池", func(c *config.Business) { c.StudentDSN = "" }, false},
		{"空上传预算", func(c *config.Business) { c.Card.MaxBytes = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testBusinessSettings(t)
			c.Runtime.DatabasePolling = false
			tc.change(&c)
			p := filepath.Join(t.TempDir(), "business.json")
			if e := os.WriteFile(p, mustJSON(t, c), 0600); e != nil {
				t.Fatal(e)
			}
			_, e := config.LoadBusiness(context.Background(), p, "production", "api")
			if (e == nil) != tc.ok {
				t.Fatal("配置准入不符", e)
			}
		})
	}
	c := testBusinessSettings(t)
	c.Runtime.DatabasePolling = false
	if _, e := config.LoadWhispers(context.Background(), writeSettings(t, c.Runtime), "production"); e == nil {
		t.Fatal("独立旧入口绕过完整宿主门禁")
	}
	c.Runtime.Enabled = false
	c.Runtime.TaskTransport = "postgres"
	if _, e := config.LoadWhispers(context.Background(), writeSettings(t, c.Runtime), "production"); e == nil {
		t.Fatal("独立旧入口擅自启用完整业务数据库任务")
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

// TestBusinessMQIsolation 生产拒绝无鉴权通道、错误领域事件和不能覆盖完整处理的超时预算。
func TestBusinessMQIsolation(t *testing.T) {
	c := testBusinessSettings(t)
	c.Runtime.DatabasePolling = false
	c.Runtime.OperationTimeout = 60 * time.Second
	for _, kind := range []string{"card", "content", "whispers-review", "whispers-unlock"} {
		var options workrunner.Config
		if e := config.LoadCardWorkerMQ("../../configs/examples/production-"+kind+"-mq.yaml", &options); e != nil {
			t.Fatal(e)
		}
		options.MQ.Credentials.AccessKey = "synthetic-access"
		options.MQ.Credentials.SecretKey = "synthetic-secret"
		path := filepath.Join(t.TempDir(), "mq.json")
		if e := os.WriteFile(path, mustJSON(t, options), 0600); e != nil {
			t.Fatal(e)
		}
		switch kind {
		case "card":
			c.CardMQFile = path
		case "content":
			c.ContentMQFile = path
		case "whispers-review":
			c.Runtime.ReviewMQFile = path
		case "whispers-unlock":
			c.Runtime.UnlockMQFile = path
		}
	}
	for _, kind := range []string{"card", "content", "whispers"} {
		if e := CheckBusinessWorkerConfig(c, "production", kind); e != nil {
			t.Fatal(kind, e)
		}
	}
	var options workrunner.Config
	if e := config.LoadCardWorkerMQ(c.CardMQFile, &options); e != nil {
		t.Fatal(e)
	}
	options.MQ.AllowUnauthenticated = true
	if e := os.WriteFile(c.CardMQFile, mustJSON(t, options), 0600); e != nil {
		t.Fatal(e)
	}
	if e := CheckBusinessWorkerConfig(c, "production", "card"); e == nil {
		t.Fatal("生产接受无鉴权标志")
	}
	c.Runtime.UnlockMQFile = c.Runtime.ReviewMQFile
	if e := CheckBusinessWorkerConfig(c, "production", "whispers"); e == nil {
		t.Fatal("审核与解锁复用通道")
	}
}
