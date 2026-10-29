package whisperapp

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// TestLocalConfiguration 验证单DSN无需外部网关、生产拒绝及预算校验仍保留。
func TestLocalConfiguration(t *testing.T) {
	cfg := testSettings()
	cfg.LocalTest = true
	cfg.DatabaseDSN = "single-test-dsn"
	cfg.JournalFile = filepath.Join(t.TempDir(), "journal.json")
	cfg.WrapKey = bytes.Repeat([]byte{31}, 32)
	cfg.RecoveryWrapKey = bytes.Repeat([]byte{32}, 32)
	cfg.GatewayURL = ""
	cfg.GatewayTokenFile = ""
	cfg.ProviderVersion = ""
	cfg.CatalogFile = ""
	cfg.KeyRefs = nil
	cfg.RecoveryKeyRefs = nil
	cfg.RecoveryWriterDSN = ""
	cfg.RecoveryReaderDSN = ""
	loaded, e := config.LoadWhispers(context.Background(), writeSettings(t, cfg), "integration")
	if e != nil {
		t.Fatal(e)
	}
	for _, dsn := range []string{loaded.APIDSN, loaded.ReviewDSN, loaded.CrisisDSN, loaded.PrivacyDSN, loaded.WorkerDSN, loaded.EvidencePurgerDSN} {
		if dsn != cfg.DatabaseDSN {
			t.Fatal("存在第二份业务连接")
		}
	}
	cfg.Enabled = false
	if _, e = config.LoadWhispers(context.Background(), writeSettings(t, cfg), "production"); e == nil {
		t.Fatal("生产接受本地模式")
	}
}

// TestLocalProvider 验证原文强制人工、不虚报通知和外部清理、空联系方式降级。
func TestLocalProvider(t *testing.T) {
	ctx := context.Background()
	p := localProvider{zap.NewNop()}
	body := "测试原文与联系方式保留"
	out, e := p.AnalyzeWhisper(ctx, "whisper_post", body, whisper.IdentityInput{Mode: "anonymous"})
	if e != nil || out.Body != body || !out.OrdinaryHit {
		t.Fatal("未进入原文人工审核", e)
	}
	if e = p.NotifyCrisis(ctx, "delivery", "case"); e == nil {
		t.Fatal("未配置外部通知却报成功")
	}
	if e = p.Erase(ctx, privacy.Intent{}, []privacy.Object{{}}); e == nil {
		t.Fatal("外部对象未清理却报成功")
	}
	if e = p.Erase(ctx, privacy.Intent{}, []privacy.Object{{ReferenceOnly: true}}); e != nil {
		t.Fatal("本地数据库引用不能被误当作外部对象", e)
	}
	r, e := loadResources(ctx, Settings{LocalTest: true})
	if e != nil || r.Care != nil || r.Catalog.Help.Status != "unavailable" {
		t.Fatal("空联系方式被伪造成可用", e)
	}
	r, e = loadResources(ctx, Settings{LocalTest: true, Contact: "测试联系信息"})
	if e != nil || r.Care == nil || r.Care.Hotline24Hours != "测试联系信息" {
		t.Fatal("简单联系方式未装配", e)
	}
}
