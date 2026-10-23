package devapp

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

// TestRuntimeCrisisPool 显式验证危机独立登录可通过启动保护并读取证据域，不授予普通池额外权限。
func TestRuntimeCrisisPool(t *testing.T) {
	path := os.Getenv("APP_DEVAPP_TEST_FILE")
	if path == "" {
		t.Skip("需显式提供隔离开发配置验证危机数据库身份")
	}
	cfg, err := Load(path)
	if err != nil || cfg.CrisisDSN == "" {
		t.Fatal("缺少有效危机开发配置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, closePool, err := pool(ctx, cfg.CrisisDSN, "liyuanci_crisis_api")
	if err != nil {
		t.Fatal("危机独立身份无法通过启动保护")
	}
	defer closePool()
	var allowed bool
	if err = db.WithContext(ctx).Raw("SELECT has_schema_privilege(current_user,'evidence','USAGE') AND has_table_privilege(current_user,'evidence.crisis_cases','SELECT') AND has_table_privilege(current_user,'evidence.evidence_items','SELECT')").Scan(&allowed).Error; err != nil || !allowed {
		t.Fatal("危机独立身份缺少限定证据读取权限")
	}
	// 即使其他身份有可用连接，也不能替代危机身份装配。
	_, closeWrong, err := pool(ctx, cfg.ReviewDSN, "liyuanci_crisis_api")
	if err == nil {
		closeWrong()
		t.Fatal("普通后台身份被错误接受为危机身份")
	}
}

// TestRuntimePoolWithoutEvidenceAccess 显式读取开发配置，以真实受限身份检查启动不要求普通池访问证据模式。
// 仅执行目录查询，不迁移、不消费任务、不创建测试账号。
func TestRuntimePoolWithoutEvidenceAccess(t *testing.T) {
	path := os.Getenv("APP_DEVAPP_TEST_FILE")
	if path == "" {
		t.Skip("需显式提供隔离开发配置验证真实数据库权限")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal("读取隔离开发配置失败")
	}
	for _, spec := range []struct{ dsn, role string }{
		{cfg.StudentDSN, "liyuanci_identity_maintenance"},
		{cfg.ReviewDSN, "liyuanci_admin_api"},
		{cfg.WishDSN, "liyuanci_api"},
		{cfg.AdminDSN, "liyuanci_admin_maintenance"},
	} {
		t.Run(spec.role, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			db, closePool, err := pool(ctx, spec.dsn, spec.role)
			if err != nil {
				t.Fatal("普通运行身份无法通过目录检查")
			}
			defer closePool()
			var allowed bool
			if err = db.WithContext(ctx).Raw("SELECT has_schema_privilege(current_user,'evidence','USAGE')").Scan(&allowed).Error; err != nil || allowed {
				t.Fatal("普通运行身份的证据域隔离发生变化")
			}
		})
	}
}

// TestContentWorkerBoundary 覆盖 T016/REQ-015/018 的生产隔离、显式合成与默认失败关闭。
func TestContentWorkerBoundary(t *testing.T) {
	cfg := Settings{WorkerDSN: "unused", WrapKey: bytes.Repeat([]byte{1}, 32), LookupKey: bytes.Repeat([]byte{2}, 32)}
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

// TestManualContentReview 验证原文和标签保留、公开文本及私聊均强制人工，取消后不产生候选。
func TestManualContentReview(t *testing.T) {
	campus := "east"
	input := wish.Content{Body: "保留待人工检查的原始正文", IdentityTags: wish.IdentityTags{Campus: &campus}}
	a := developmentAnalyzer{logger: zap.NewNop(), manual: true}
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
