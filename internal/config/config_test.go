package config

import (
	"fmt"
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

// T008/REQ-018、019：默认不启用后台，不为待定TTL/限流或凭据文件提供隐含值。
func TestAdminExplicitConfiguration(t *testing.T) {
	c, err := Load("")
	if err != nil || c.AdminAuth.Enabled || c.AdminAuth.SessionTTL != 0 {
		t.Fatal("后台默认不应启用")
	}
	t.Setenv("APP_ADMINAUTH_ENABLED", "true")
	if _, err = Load(""); err == nil {
		t.Fatal("缺失部署参数不能启用")
	}
	for key, value := range map[string]string{
		"APP_ADMINAUTH_DSN":                 "synthetic-dsn",
		"APP_ADMINAUTH_INITIALPASSWORDFILE": "/synthetic/password",
		"APP_ADMINAUTH_RECEIPTKEYFILE":      "/synthetic/key",
		"APP_REDIS_ENABLED":                 "true",
		"APP_ADMINAUTH_SESSIONTTL":          "1h",
		"APP_ADMINAUTH_TIMEOUT":             "10s",
		"APP_ADMINAUTH_LOCKTIMEOUT":         "1s",
		"APP_ADMINAUTH_RECEIPTTTL":          "1h",
		"APP_ADMINAUTH_LOGINWINDOW":         "1m",
		"APP_ADMINAUTH_LOGINLIMIT":          "10",
		"APP_ADMINAUTH_MAXATTEMPTS":         "3",
	} {
		t.Setenv(key, value)
	}
	c, err = Load("")
	if err != nil || !c.AdminAuth.Enabled {
		t.Fatal("显式隔离测试配置应可加载", err)
	}
	t.Setenv("APP_ADMINAUTH_SESSIONTTL", "0s")
	if _, err = Load(""); err == nil {
		t.Fatal("未定TTL不得自行选择")
	}
}

// TestDevAdaptersRejectProduction 追溯 T008/T011/T014、REQ-018：本地开发资源不能通过环境配置进入生产或基础模式。
func TestDevAdaptersRejectProduction(t *testing.T) {
	t.Setenv("APP_DEVAPPFILE", "/outside/business.json")
	if _, err := Load(""); err == nil {
		t.Fatal("基础模式不能装配开发业务")
	}
	t.Setenv("APP_ENVIRONMENT", "production")
	t.Setenv("APP_DATABASE_DRIVER", "postgres")
	t.Setenv("APP_DATABASE_DSN", "unused")
	t.Setenv("APP_REDIS_ENABLED", "true")
	t.Setenv("APP_ROCKETMQ_ENABLED", "true")
	if _, err := Load(""); err == nil {
		t.Fatal("生产不能装配本地开发资源")
	}
	t.Setenv("APP_ENVIRONMENT", "integration")
	if _, err := Load(""); err != nil {
		t.Fatal(err)
	}
}

// TestDevelopmentWithoutMQ 验证显式开发业务可以仅使用PG/Redis，完整集成和生产不降级。
func TestDevelopmentWithoutMQ(t *testing.T) {
	for _, tc := range []struct {
		name, environment, file string
		redis, wantOK           bool
	}{
		{"local", "integration", "/outside/business.host.json", true, true},
		{"full integration", "integration", "", true, false},
		{"production", "production", "/outside/business.host.json", true, false},
		{"missing redis", "integration", "/outside/business.host.json", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENVIRONMENT", tc.environment)
			t.Setenv("APP_DEVAPPFILE", tc.file)
			t.Setenv("APP_DATABASE_DRIVER", "postgres")
			t.Setenv("APP_DATABASE_DSN", "devapp")
			t.Setenv("APP_REDIS_ENABLED", fmt.Sprint(tc.redis))
			t.Setenv("APP_ROCKETMQ_ENABLED", "false")
			_, err := Load("")
			if (err == nil) != tc.wantOK {
				t.Fatalf("配置判定错误: %v", err)
			}
		})
	}
}
