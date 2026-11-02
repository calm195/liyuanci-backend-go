package local

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Analyzer 仅用于开发环境的人工或显式合成分析；零值模式不放行内容。
type Analyzer struct {
	Logger    *zap.Logger
	Synthetic bool
	Manual    bool
}

// Analyze 在人工模式保留原文待审，在合成模式清除正文及身份标签。
func (a Analyzer) Analyze(ctx context.Context, input wish.Content) (out moderation.Assessment, err error) {
	defer completed(a.Logger, "development_content_analysis", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	// 调用本适配器前已完成词库筛选；人工模式对命中或重审保留原文/标签，生成必须复核候选。
	if a.Manual {
		a.Logger.Info("开发内容提交人工复核", zap.Bool("manual_review", true), zap.String("result", "success"))
		return moderation.Assessment{Body: input.Body, Tags: input.IdentityTags, OrdinaryHit: true, DetectorVersion: "development-manual-v1"}, nil
	}
	if !a.Synthetic {
		return out, command.ErrUnavailable
	}
	// 合成模式清除所有原始正文与标签；只验证技术链路，不模拟隐私识别能力。
	a.Logger.Info("开发内容分析使用合成结果", zap.Bool("simulated", true), zap.String("result", "success"))
	return moderation.Assessment{Body: "[开发合成审核正文]", DetectorVersion: "development-synthetic-v1"}, nil
}

// AnalyzeMessage 保留私聊原文；合成模式只产生分类，人工模式对命中/重审强制交人工复核。
func (a Analyzer) AnalyzeMessage(ctx context.Context, _ string) (out moderation.MessageAssessment, err error) {
	defer completed(a.Logger, "development_message_analysis", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if a.Manual {
		return moderation.MessageAssessment{OrdinaryHit: true, DetectorVersion: "development-manual-private-v1"}, nil
	}
	if !a.Synthetic {
		return out, command.ErrUnavailable
	}
	return moderation.MessageAssessment{DetectorVersion: "development-synthetic-private-v1"}, nil
}
