package app

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/config"
)

// TestLocalConfiguration 验证单DSN无需外部网关、生产拒绝及预算校验仍保留。
func TestLocalConfiguration(t *testing.T) {
	cfg := testSettings()
	cfg.LocalTest = true
	cfg.JournalFile = filepath.Join(t.TempDir(), "journal.json")
	cfg.WrapKey = bytes.Repeat([]byte{31}, 32)
	cfg.RecoveryWrapKey = bytes.Repeat([]byte{32}, 32)
	cfg.ResourcesFile = ""
	document, e := config.Load(writeSettings(t, cfg))
	if e != nil {
		t.Fatal(e)
	}
	loaded, e := config.ValidateWhispers(document.Whispers, "integration")
	if e != nil {
		t.Fatal(e)
	}
	if databaseSettings(loaded.DatabaseDSN) != databaseSettings(cfg.DatabaseDSN) {
		t.Fatal("分项配置改变业务连接")
	}
	cfg.Enabled = false
	document, e = config.Load(writeSettings(t, cfg))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = config.ValidateWhispers(document.Whispers, "production"); e == nil {
		t.Fatal("生产接受本地模式")
	}
}

// TestLocalResources 验证本地空资料降级，不生成未经核验的默认联系资料。
func TestLocalResources(t *testing.T) {
	ctx := context.Background()
	r, e := loadResources(ctx, RuntimeSettings{LocalTest: true})
	if e != nil || r.Care != nil || r.Catalog.Help.Status != "unavailable" {
		t.Fatal("空联系方式被伪造成可用", e)
	}

}
