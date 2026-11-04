package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T, base, overlay string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"common.yaml": base, "development.yaml": overlay} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "development.yaml")
}

// TestConfigurationLayers 验证公共默认、环境覆盖、嵌套保留、单DSN注入和相对路径。
func TestConfigurationLayers(t *testing.T) {
	path := fixture(
		t,
		"mode: development\ndatabase:\n  driver: postgres\n  maxopen: 6\nredis:\n  enabled: true\nserver:\n  readtimeout: 8s\nbusiness:\n  runtime:\n    journalfile: ../state/deletions.json\n",
		"masterkey: "+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32))+"\nenvironment: development\ndatabase:\n  host: 127.0.0.1\n  name: shared\n  user: application\n  password: synthetic\nserver:\n  address: 127.0.0.1:9000\n",
	)
	t.Setenv("APP_DATABASE_DSN", "ignored")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.DSN == "" || c.Business.Runtime.DatabaseDSN != c.Database.DSN || c.Whispers.DatabaseDSN != c.Database.DSN ||
		c.Database.MaxOpen != 6 ||
		c.Server.ReadTimeout != 8*time.Second {
		t.Fatal("覆盖或单DSN失效")
	}
	if c.Business.Runtime.JournalFile != filepath.Clean(filepath.Join(filepath.Dir(path), "../state/deletions.json")) {
		t.Fatal("相对路径不以配置目录解析")
	}
}

// TestConfigurationRejectsLegacyAndInvalid 拒绝已移除通道、第二DSN、重复键和生产开发混用。
func TestConfigurationRejectsLegacyAndInvalid(t *testing.T) {
	for _, overlay := range []string{

		"development: {}\n",
		"development:\n  adminpassword: obsolete\n",
		"rocketmq:\n  enabled: true\n",
		"businessfile: old.yaml\n",
		"business:\n  studentdsn: second\n",
		"business:\n  runtime:\n    databasedsn: second\n",
		"database:\n  dsn: first\n  DSN: second\n",
		"database:\n  dsn: null\n",
		"database:\n  dsn: old\n",
		"database:\n  dsn: ''\n",
		"unknown: true\n",
		"server:\n  readtimeout: 10\n",
		"environment: production\nmode: development\n",
		"mode: development\n",
		"server:\n  address: first\n  address: second\n",
	} {
		t.Run(overlay, func(t *testing.T) {
			if _, e := Load(fixture(t, "mode: foundation\n", overlay)); e == nil {
				t.Fatal("接受非法配置")
			}
		})
	}
}

func TestConfigurationRequiresCommon(t *testing.T) {
	p := filepath.Join(t.TempDir(), "production.yaml")
	if e := os.WriteFile(p, []byte("mode: foundation\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(p); e == nil {
		t.Fatal("缺少公共配置仍启动")
	}
}
