package local

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// TestLocalProvider 验证原文强制人工、不虚报通知和外部清理、空联系方式降级。
func TestLocalProvider(t *testing.T) {
	ctx := context.Background()
	p := WhisperProvider{zap.NewNop()}
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
}
