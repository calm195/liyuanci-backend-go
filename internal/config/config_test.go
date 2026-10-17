package config

import (
	"os"
	"path/filepath"
	"testing"
)

// T004/REQ-018、020：保留模板配置与安全日志质量回归。
func TestLoadEnvironmentOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  address: ':9000'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_SERVER_ADDRESS", ":9100")
	t.Setenv("APP_REDIS_ENABLED", "true")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Address != ":9100" || !c.Redis.Enabled {
		t.Fatalf("environment not applied: %+v", c.Server)
	}
}

// T004/REQ-018、020：保留模板配置与安全日志质量回归。
func TestLoadRejectsInvalidDriver(t *testing.T) {
	t.Setenv("APP_DATABASE_DRIVER", "sqlite")
	if _, err := Load(""); err == nil {
		t.Fatal("expected invalid driver error")
	}
}

// T004/REQ-018、020：保留模板配置与安全日志质量回归。
func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected missing file error")
	}
}

// TestRealEnvironmentFailsClosed 验证 T004/REQ-018、020：生产与隔离联调不可使用缺失中间件的默认配置。
// T004/REQ-018、020：保留模板配置与安全日志质量回归。
func TestRealEnvironmentFailsClosed(t *testing.T) {
	for _, env := range []string{"production", "integration", "mock"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("APP_ENVIRONMENT", env)
			if _, err := Load(""); err == nil {
				t.Fatal("expected fail closed")
			}
		})
	}
	t.Setenv("APP_DATABASE_DRIVER", "mysql")
	if _, err := Load(""); err == nil {
		t.Fatal("only PostgreSQL is supported")
	}
}
