package moderation

import (
	"context"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// analyze 只在词库命中或显式重审时调用原分析器。未命中候选保留原文和显式标签，
// 不在此处写状态；消费者继续按原来的版本、资格、撤回与持久回执规则提交。
func (w *Worker) analyze(ctx context.Context, source current, snapshot rules, input wish.Content) (out Assessment, err error) {
	if err := ctx.Err(); err != nil {
		return Assessment{}, err
	}
	evaluation, err := snapshot.scan(input.Body)
	if err != nil {
		return Assessment{}, err
	}
	recheck := source.task.PrivacyRecheck || source.task.CrisisRecheck
	school := evaluation.routesTo("school")
	defer func() {
		if err == nil {
			out.wordlistEvaluation = &evaluation
			out.CrisisHit = out.CrisisHit || evaluation.routesTo("crisis")
			out.SchoolRelated = source.message == nil && (out.SchoolRelated || school)
			out.OrdinaryHit = out.OrdinaryHit || recheck || evaluation.routesTo("ordinary_violation") ||
				source.message != nil && school
		}
	}()
	if !recheck && len(evaluation.Routes) == 0 {
		return Assessment{Body: input.Body, Tags: input.IdentityTags, DetectorVersion: wordlistDetectorVersion}, nil
	}
	if source.message != nil {
		if w.cfg.MessageAnalyzer == nil {
			return Assessment{}, command.ErrUnavailable
		}
		result, err := w.cfg.MessageAnalyzer.AnalyzeMessage(ctx, input.Body)
		return Assessment{
			Body:            input.Body,
			OrdinaryHit:     result.OrdinaryHit,
			CrisisHit:       result.CrisisHit,
			DetectorVersion: result.DetectorVersion,
		}, err
	}
	return w.analyzer.Analyze(ctx, input)
}

// analyze 对胶囊仅分类，不为私密正文创建公开替换投影。
func (w *WhisperWorker) analyze(ctx context.Context, source whisperSource, snapshot rules, body string, identity whisper.IdentityInput) (out WhisperAssessment, err error) {
	if err := ctx.Err(); err != nil {
		return WhisperAssessment{}, err
	}
	evaluation, err := snapshot.scan(body)
	if err != nil {
		return WhisperAssessment{}, err
	}
	recheck := source.task.PrivacyRecheck || source.task.CrisisRecheck
	school := evaluation.routesTo("school")
	capsule := source.registry.Kind == "whisper_capsule"
	defer func() {
		if err == nil {
			out.wordlistEvaluation = &evaluation
			out.CrisisHit = out.CrisisHit || evaluation.routesTo("crisis")
			out.OrdinaryHit = out.OrdinaryHit || recheck || evaluation.routesTo("ordinary_violation") || capsule && school
			out.SchoolRelated = !capsule && (out.SchoolRelated || school)
		}
	}()
	if !recheck && len(evaluation.Routes) == 0 {
		if source.registry.Kind == "whisper_capsule" {
			return WhisperAssessment{DetectorVersion: wordlistDetectorVersion}, nil
		}
		return WhisperAssessment{Body: body, Identity: identity, DetectorVersion: wordlistDetectorVersion}, nil
	}
	if source.registry.Kind == "whisper_capsule" {
		result, err := w.private.ClassifyWhisperCapsule(ctx, body)
		return WhisperAssessment{
			OrdinaryHit:     result.OrdinaryHit,
			CrisisHit:       result.CrisisHit,
			DetectorVersion: result.DetectorVersion,
		}, err
	}
	return w.public.AnalyzeWhisper(ctx, source.registry.Kind, body, identity)
}
