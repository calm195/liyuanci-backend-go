package devapp

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

// TestContentWorkerBoundary 覆盖 T016/REQ-015/018 的生产隔离、显式合成与默认失败关闭。
func TestContentWorkerBoundary(t *testing.T) {
	cfg := Settings{WorkerDSN: "unused", WrapKey: bytes.Repeat([]byte{1}, 32), LookupKey: bytes.Repeat([]byte{2}, 32)}
	for _, tc := range []struct {
		name, environment, mode string
		valid                   bool
	}{
		{"生产拒绝", "production", "synthetic", false},
		{"默认不放行", "integration", "", true},
		{"缺供应商", "integration", "unavailable", true},
		{"显式合成", "integration", "synthetic", true},
		{"未知模式", "integration", "approve", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := ContentWorkerConfig{Analyzer: tc.mode}
			if (ValidateContentWorker(tc.environment, cfg, options) == nil) != tc.valid {
				t.Fatal("启动边界错误")
			}
			if !tc.valid && RunContentWorker(context.Background(), tc.environment, cfg, options, zap.NewNop()) != command.ErrInvalid {
				t.Fatal("无效配置访问了数据库")
			}
		})
	}
	if RunContentWorker(context.Background(), "integration", cfg, ContentWorkerConfig{}, nil) != command.ErrInvalid {
		t.Fatal("缺日志仍启动")
	}
	input := wish.Content{Body: "合成敏感正文"}
	if out, err := (developmentAnalyzer{logger: zap.NewNop()}).Analyze(context.Background(), input); err == nil || out.Body != "" {
		t.Fatal("未配置供应商被放行")
	}
	a := developmentAnalyzer{logger: zap.NewNop(), synthetic: true}
	out, err := a.Analyze(context.Background(), input)
	if err != nil || out.Body == input.Body || out.DetectorVersion != "development-synthetic-v1" || out.Tags != (wish.IdentityTags{}) {
		t.Fatal("合成模式泄漏原文或标签")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err = a.Analyze(ctx, input); err == nil || out != (moderation.Assessment{}) {
		t.Fatal("取消后产生结论")
	}
	options := ContentWorkerConfig{MQ: &workrunner.Config{OperationTimeout: time.Second}}
	if ValidateContentWorker("integration", cfg, options) == nil {
		t.Fatal("无效消息配置被接受")
	}
}
