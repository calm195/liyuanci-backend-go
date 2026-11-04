package local

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// Analyzer 对词库命中或显式重审保留原文并要求人工复核；词库筛选在领域内完成。
type Analyzer struct{ Logger *zap.Logger }

// Analyze 保留原文及显式身份标签待审。
func (a Analyzer) Analyze(ctx context.Context, input wish.Content) (out moderation.Assessment, err error) {
	defer completed(a.Logger, "content_manual_review", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return moderation.Assessment{
		Body:            input.Body,
		Tags:            input.IdentityTags,
		OrdinaryHit:     true,
		DetectorVersion: "local-manual-v1",
	}, nil
}

// AnalyzeMessage 只返回人工复核分类，不返回私聊正文替换。
func (a Analyzer) AnalyzeMessage(ctx context.Context, _ string) (out moderation.MessageAssessment, err error) {
	defer completed(a.Logger, "message_manual_review", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return moderation.MessageAssessment{OrdinaryHit: true, DetectorVersion: "local-manual-private-v1"}, nil
}
