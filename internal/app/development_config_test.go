package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// TestSettingsRejectKeyReuse 验证单主密钥装配后领域仍拒绝错误注入共钥，文件拒绝半套微信凭据。
func TestSettingsRejectKeyReuse(t *testing.T) {
	cfg := DevelopmentSettings{
		DatabaseDSN:     "shared",
		AdminPassword:   "long-enough-password",
		ObjectDirectory: t.TempDir(),
		JournalFile:     "/tmp/unit-journal.json",
	}
	path := filepath.Join(t.TempDir(), "business.yaml")
	save := func() {
		_ = os.WriteFile(filepath.Join(filepath.Dir(path), "common.yaml"), []byte("mode: foundation\n"), 0600)
		data, _ := yamlconfig.Marshal(map[string]any{
			"masterkey": bytes.Repeat([]byte{93}, 32),
			"database": map[string]any{
				"driver":   "postgres",
				"host":     "127.0.0.1",
				"name":     "synthetic",
				"user":     "application",
				"password": "synthetic",
			},
			"development": cfg,
		})
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	loaded, err := LoadDevelopment(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg = loaded
	cfg.CardKey = cfg.SessionKey
	if _, err := validateDevelopment(cfg); err == nil {
		t.Fatal("复用密钥未拒绝")
	}
	cfg.CardKey = bytes.Repeat([]byte{4}, 32)
	cfg.WeChatAppID = "app"
	save()
	if _, err := LoadDevelopment(path); err == nil {
		t.Fatal("半套微信配置未拒绝")
	}
}
