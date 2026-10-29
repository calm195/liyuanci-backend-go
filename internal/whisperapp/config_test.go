package whisperapp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
)

func testSettings() Settings {
	cfg := Settings{Enabled: true, APIDSN: "api", ReviewDSN: "review", CrisisDSN: "crisis", PrivacyDSN: "privacy", EvidenceDeletionDSN: "evidence", WorkerDSN: "worker", EvidencePurgerDSN: "purger", RecoveryWriterDSN: "writer", RecoveryReaderDSN: "reader", GatewayURL: "https://gateway.invalid/rpc", GatewayTokenFile: "token", ProviderVersion: "test-v1", KeyRefs: []string{"business-v1"}, RecoveryKeyRefs: []string{"journal-v1"}, CatalogFile: "catalog", DatabasePolling: true, Command: command.Config{Timeout: time.Second, LockTimeout: time.Millisecond, ReceiptTTL: time.Hour, MaxAttempts: 2}, Interval: 10 * time.Millisecond, OperationTimeout: time.Second, ProviderTimeout: time.Second}
	cfg.Worker.Consumer = durable.ConsumerConfig{Timeout: 100 * time.Millisecond, LockTimeout: time.Millisecond, RetryDelay: time.Second, MaxAttempts: 2}
	cfg.Worker.AnalysisTimeout = 100 * time.Millisecond
	keys := []*[]byte{&cfg.ReceiptKey, &cfg.CursorKey, &cfg.RateKey, &cfg.GovernanceKey, &cfg.PrivacyKey, &cfg.EvidenceKey, &cfg.SafetyKey}
	for i, k := range keys {
		*k = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}
	return cfg
}
func writeSettings(t *testing.T, cfg Settings) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.json")
	raw, e := json.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

// TestRuntimeConfiguration 覆盖B08的生产关闭、密钥隔离、期限及供应商配置失败关闭。
func TestRuntimeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		change    func(*Settings)
		valid     bool
	}{
		{"显式集成", "integration", func(*Settings) {}, true},
		{"生产关闭", "production", func(c *Settings) { c.Enabled = false; c.DatabasePolling = false }, true},
		{"生产开启拒绝", "production", func(*Settings) {}, false},
		{"基础模式不装配", "foundation", func(*Settings) {}, false},
		{"复用密钥拒绝", "integration", func(c *Settings) { c.CursorKey = c.ReceiptKey }, false},
		{"缺供应商", "integration", func(c *Settings) { c.ProviderVersion = "" }, false},
		{"复用恢复密钥", "integration", func(c *Settings) { c.RecoveryKeyRefs = c.KeyRefs }, false},
		{"无总时限", "integration", func(c *Settings) { c.OperationTimeout = 0 }, false},
		{"预算无法覆盖扫描", "integration", func(c *Settings) { c.OperationTimeout = time.Millisecond }, false},
		{"无日志库", "integration", func(c *Settings) { c.RecoveryReaderDSN = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testSettings()
			tc.change(&cfg)
			_, e := config.LoadWhispers(context.Background(), writeSettings(t, cfg), tc.env)
			if (e == nil) != tc.valid {
				t.Fatal(e)
			}
		})
	}
	cfg := testSettings()
	path := writeSettings(t, cfg)
	file, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = file.WriteString(" {}")
	_ = file.Close()
	if _, e = config.LoadWhispers(context.Background(), path, "integration"); e == nil {
		t.Fatal("接受尾随JSON")
	}
	if _, e = workerOptions(cfg, "production"); e == nil {
		t.Fatal("生产接受数据库轮询模式")
	}
}
