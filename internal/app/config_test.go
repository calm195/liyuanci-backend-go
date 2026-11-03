package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

func testSettings() RuntimeSettings {
	cfg := RuntimeSettings{
		Enabled:         true,
		DatabaseDSN:     "host=127.0.0.1 port=5432 dbname=synthetic user=application password=synthetic sslmode=disable",
		ResourcesFile:   "catalog",
		JournalFile:     "/tmp/test-journal.json",
		Command:         config.Command{Timeout: time.Second, LockTimeout: time.Millisecond, ReceiptTTL: time.Hour, MaxAttempts: 2},
		ProviderTimeout: time.Second,
	}
	cfg.Tasks.ScanInterval, cfg.Tasks.OperationTimeout = 10*time.Millisecond, time.Second
	cfg.DatabaseMaxOpen, cfg.DatabaseMaxIdle = 10, 2
	cfg.Tasks.Consumer = config.Consumer{
		Timeout:     100 * time.Millisecond,
		LockTimeout: time.Millisecond,
		RetryDelay:  time.Second,
		MaxAttempts: 2,
	}
	cfg.Tasks.AnalysisTimeout = 100 * time.Millisecond
	keys := []*[]byte{
		&cfg.WrapKey,
		&cfg.RecoveryWrapKey,
		&cfg.ReceiptKey,
		&cfg.CursorKey,
		&cfg.RateKey,
		&cfg.GovernanceKey,
		&cfg.PrivacyKey,
		&cfg.EvidenceKey,
		&cfg.SafetyKey,
	}
	for i, k := range keys {
		*k = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}
	return cfg
}

func writeSettings(t *testing.T, cfg RuntimeSettings) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	_ = os.WriteFile(filepath.Join(filepath.Dir(path), "common.yaml"), []byte("mode: foundation\n"), 0600)
	doc := config.Defaults()
	doc.MasterKey = bytes.Repeat([]byte{91}, 32)
	doc.Database = databaseSettings(cfg.DatabaseDSN)
	doc.Whispers = cfg
	raw, e := yamlconfig.Marshal(doc)
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
		change    func(*RuntimeSettings)
		valid     bool
	}{
		{"显式集成", "integration", func(*RuntimeSettings) {}, true},
		{"生产关闭", "production", func(c *RuntimeSettings) { c.Enabled = false }, true},
		{"生产开启拒绝", "production", func(*RuntimeSettings) {}, false},
		{"基础模式不装配", "foundation", func(*RuntimeSettings) {}, false},
		{"无总时限", "integration", func(c *RuntimeSettings) { c.Tasks.OperationTimeout = 0 }, false},
		{"预算无法覆盖扫描", "integration", func(c *RuntimeSettings) { c.Tasks.OperationTimeout = time.Millisecond }, false},
		{"锁等待超过事务", "integration", func(c *RuntimeSettings) { c.Tasks.Consumer.LockTimeout = time.Second }, false},
		{"重试次数超限", "integration", func(c *RuntimeSettings) { c.Tasks.Consumer.MaxAttempts = 1001 }, false},
		{"无日志库", "integration", func(c *RuntimeSettings) { c.DatabaseDSN = "" }, false},
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
	_, _ = file.WriteString("\n---\nEnabled: true\n")
	_ = file.Close()
	if _, e = config.LoadWhispers(context.Background(), path, "integration"); e == nil {
		t.Fatal("接受多文档YAML")
	}

}

// mustYAML 为进程配置生成带单位期限和Base64密钥的YAML，网络协议仍使用JSON。
func mustYAML(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := yamlconfig.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func businessDocument(c config.Business) config.Config {
	out := config.Defaults()
	out.MasterKey = bytes.Repeat([]byte{92}, 32)
	out.Mode, out.Environment = "business", "production"
	out.Database = databaseSettings(c.Runtime.DatabaseDSN)
	out.Database.MaxOpen, out.Database.MaxIdle = c.Runtime.DatabaseMaxOpen, c.Runtime.DatabaseMaxIdle
	out.Redis.Enabled, out.Redis.Password = true, "synthetic-password"
	out.Business = c
	return out
}

// databaseSettings 只在测试中把临时实例连接转为应用分项配置；应用不保留DSN配置入口。
func databaseSettings(dsn string) config.Database {
	d := config.Defaults().Database
	if dsn == "" {
		return d
	}
	parsed, err := pgconn.ParseConfig(dsn)
	if err != nil {
		panic("隔离测试连接无效")
	}
	d.Driver, d.Host, d.Port = "postgres", parsed.Host, int(parsed.Port)
	d.Name, d.User, d.Password = parsed.Database, parsed.User, parsed.Password
	return d
}
