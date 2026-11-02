package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// TestRuntimeSharedDatabase 验证唯一配置账号具备领域表访问且没有集群管理权限。
func TestRuntimeSharedDatabase(t *testing.T) {
	path := testconfig.Value("devapp_test_file")
	if path == "" {
		t.Skip("需显式隔离开发配置")
	}
	cfg, e := LoadDevelopment(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, closePool, e := cfg.pool(ctx, zap.NewNop())
	if e != nil {
		t.Fatal(e)
	}
	defer closePool()
	var allowed bool
	if db.WithContext(ctx).Raw("SELECT has_table_privilege(current_user,'public.accounts','SELECT') AND has_table_privilege(current_user,'evidence.crisis_cases','SELECT') AND NOT r.rolsuper AND NOT r.rolbypassrls FROM pg_roles r WHERE rolname=current_user").Scan(&allowed).Error != nil || !allowed {
		t.Fatal("应用账号权限不符合单账号配置")
	}
}

// TestContentWorkerBoundary 覆盖 T016/REQ-015/018 的生产隔离、显式合成与默认失败关闭。
func TestContentWorkerBoundary(t *testing.T) {
	cfg := DevelopmentSettings{DatabaseDSN: "unused", WrapKey: bytes.Repeat([]byte{1}, 32), LookupKey: bytes.Repeat([]byte{2}, 32)}
	for _, tc := range []struct {
		name, environment, mode string
		valid                   bool
	}{
		{"生产拒绝", "production", "synthetic", false},
		{"生产拒绝人工模式", "production", "manual", false},
		{"默认不放行", "integration", "", true},
		{"缺供应商", "integration", "unavailable", true},
		{"显式合成", "integration", "synthetic", true},
		{"显式人工", "integration", "manual", true},
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
	if out, err := (local.Analyzer{Logger: zap.NewNop()}).Analyze(context.Background(), input); err == nil || out.Body != "" {
		t.Fatal("未配置供应商被放行")
	}
	a := local.Analyzer{Logger: zap.NewNop(), Synthetic: true}
	out, err := a.Analyze(context.Background(), input)
	if err != nil || out.Body == input.Body || out.DetectorVersion != "development-synthetic-v1" || out.Tags != (wish.IdentityTags{}) {
		t.Fatal("合成模式泄漏原文或标签")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err = a.Analyze(ctx, input); err == nil || out != (moderation.Assessment{}) {
		t.Fatal("取消后产生结论")
	}

}

// TestManualContentReview 验证原文和标签保留、公开文本及私聊均强制人工，取消后不产生候选。
func TestManualContentReview(t *testing.T) {
	campus := "east"
	input := wish.Content{Body: "保留待人工检查的原始正文", IdentityTags: wish.IdentityTags{Campus: &campus}}
	a := local.Analyzer{Logger: zap.NewNop(), Manual: true}
	out, err := a.Analyze(context.Background(), input)
	if err != nil || out.Body != input.Body || out.Tags.Campus == nil || *out.Tags.Campus != campus || !out.OrdinaryHit || out.CrisisHit || out.DetectorVersion != "development-manual-v1" {
		t.Fatal("人工候选未保留原文标签或没有强制复核")
	}
	message, err := a.AnalyzeMessage(context.Background(), input.Body)
	if err != nil || !message.OrdinaryHit || message.CrisisHit || message.DetectorVersion != "development-manual-private-v1" {
		t.Fatal("私聊未强制人工复核")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err = a.Analyze(ctx, input); err == nil || out != (moderation.Assessment{}) {
		t.Fatal("取消后产生原文候选")
	}
	if message, err = a.AnalyzeMessage(ctx, input.Body); err == nil || message != (moderation.MessageAssessment{}) {
		t.Fatal("取消后产生私聊判定")
	}
}
