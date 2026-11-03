package local

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

// TestLocalProvider 验证原文强制人工复核。
func TestLocalProvider(t *testing.T) {
	ctx := context.Background()
	p := WhisperProvider{zap.NewNop()}
	body := "测试原文与联系方式保留"
	out, e := p.AnalyzeWhisper(ctx, "whisper_post", body, whisper.IdentityInput{Mode: "anonymous"})
	if e != nil || out.Body != body || !out.OrdinaryHit {
		t.Fatal("未进入原文人工审核", e)
	}
}
