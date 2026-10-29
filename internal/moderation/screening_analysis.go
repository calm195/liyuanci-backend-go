package moderation

import (
	"context"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// analyze 只在词库命中或显式重审时调用原分析器。未命中候选保留原文和显式标签，
// 不在此处写状态；消费者继续按原来的版本、资格、撤回与持久回执规则提交。
func (w *Worker) analyze(ctx context.Context, source current, snapshot rules, input wish.Content) (out Assessment, err error) {
	defer func() {
		if err == nil {
			out.CrisisHit = out.CrisisHit || hit(input.Body, snapshot.crisisEntries)
			out.SchoolRelated = source.message == nil && (out.SchoolRelated || hit(input.Body, snapshot.schoolEntries))
			out.OrdinaryHit = out.OrdinaryHit || source.task.PrivacyRecheck || source.task.CrisisRecheck || hit(input.Body, snapshot.ordinaryEntries) || source.message != nil && hit(input.Body, snapshot.schoolEntries)
		}
	}()
	if err := ctx.Err(); err != nil {
		return Assessment{}, err
	}
	if !snapshot.requiresReview(input.Body, source.task.PrivacyRecheck || source.task.CrisisRecheck) {
		return Assessment{Body: input.Body, Tags: input.IdentityTags, DetectorVersion: wordlistDetectorVersion}, nil
	}
	if source.message != nil {
		if w.cfg.MessageAnalyzer == nil {
			return Assessment{}, command.ErrUnavailable
		}
		result, err := w.cfg.MessageAnalyzer.AnalyzeMessage(ctx, input.Body)
		return Assessment{Body: input.Body, OrdinaryHit: result.OrdinaryHit, CrisisHit: result.CrisisHit, DetectorVersion: result.DetectorVersion}, err
	}
	return w.analyzer.Analyze(ctx, input)
}

// analyze 对胶囊仅分类，不为私密正文创建公开替换投影。
func (w *WhisperWorker) analyze(ctx context.Context, source whisperSource, snapshot rules, body string, identity whisper.IdentityInput) (out WhisperAssessment, err error) {
	defer func() {
		if err == nil {
			out.CrisisHit = out.CrisisHit || hit(body, snapshot.crisisEntries)
			out.OrdinaryHit = out.OrdinaryHit || source.task.PrivacyRecheck || source.task.CrisisRecheck || hit(body, snapshot.ordinaryEntries) || source.registry.Kind == "whisper_capsule" && hit(body, snapshot.schoolEntries)
			out.SchoolRelated = source.registry.Kind != "whisper_capsule" && (out.SchoolRelated || hit(body, snapshot.schoolEntries))
		}
	}()
	if err := ctx.Err(); err != nil {
		return WhisperAssessment{}, err
	}
	if !snapshot.requiresReview(body, source.task.PrivacyRecheck || source.task.CrisisRecheck) {
		if source.registry.Kind == "whisper_capsule" {
			return WhisperAssessment{DetectorVersion: wordlistDetectorVersion}, nil
		}
		return WhisperAssessment{Body: body, Identity: identity, DetectorVersion: wordlistDetectorVersion}, nil
	}
	if source.registry.Kind == "whisper_capsule" {
		result, err := w.private.ClassifyWhisperCapsule(ctx, body)
		return WhisperAssessment{OrdinaryHit: result.OrdinaryHit, CrisisHit: result.CrisisHit, DetectorVersion: result.DetectorVersion}, err
	}
	return w.public.AnalyzeWhisper(ctx, source.registry.Kind, body, identity)
}
