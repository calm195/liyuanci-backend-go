package local

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

// WhisperProvider 在词库命中或重审分支保留原文并强制人工审核，不借本地测试模式声称已完成外部扫描、通知或副本删除。
type WhisperProvider struct{ Logger *zap.Logger }

// Ready 仅检查本地上下文，不代表外部供应商已就绪。
func (p WhisperProvider) Ready(ctx context.Context) error { return ctx.Err() }

// Close 无需释放外部连接，本地供应商不拥有数据库。
func (p WhisperProvider) Close() {}

// AnalyzeWhisper 保留原文并强制人工复核，不生成已审核成功结果。
func (p WhisperProvider) AnalyzeWhisper(ctx context.Context, kind, body string, identity whisper.IdentityInput) (out moderation.WhisperAssessment, err error) {
	defer finishWhisper(p.Logger, "local_public_manual_review", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return moderation.WhisperAssessment{Body: body, Identity: identity, OrdinaryHit: true, DetectorVersion: "local-manual-v1"}, nil
}

// ClassifyWhisperCapsule 将胶囊分类交人工处理，不替换正文。
func (p WhisperProvider) ClassifyWhisperCapsule(ctx context.Context, body string) (out moderation.MessageAssessment, err error) {
	defer finishWhisper(p.Logger, "local_private_manual_review", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return moderation.MessageAssessment{OrdinaryHit: true, DetectorVersion: "local-manual-private-v1"}, nil
}

// Redact 将内容恢复为待重新编辑状态，不模拟真实供应商识别。
func (p WhisperProvider) Redact(ctx context.Context, kind, body string) (out string, err error) {
	defer finishWhisper(p.Logger, "local_content_reset", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return "原内容已移除，请重新编辑后提交审核。", nil
}

// Erase 只允许没有外部实体的引用清理；存在对象时继续保持待办。
func (p WhisperProvider) Erase(ctx context.Context, in privacy.Intent, objects []privacy.Object) (err error) {
	defer finishWhisper(p.Logger, "local_cleanup", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	// 无外部对象的纯数据库测试可以继续清理；存在学生卡等对象时保留processing。
	for _, object := range objects {
		if !object.ReferenceOnly {
			return command.ErrUnavailable
		}
	}
	return nil
}

// NotifyCrisis 明确返回外部通知未配置，不虚报通知完成。
func (p WhisperProvider) NotifyCrisis(ctx context.Context, delivery, caseID string) (err error) {
	defer finishWhisper(p.Logger, "local_notification_unconfigured", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return command.ErrUnavailable
}
