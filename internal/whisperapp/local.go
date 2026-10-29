package whisperapp

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// localProvider 在词库命中或重审分支保留原文并强制人工审核，不借本地测试模式声称已完成外部扫描、通知或副本删除。
type localProvider struct{ log *zap.Logger }

func (p localProvider) ready(ctx context.Context) error { return ctx.Err() }
func (p localProvider) close()                          {}
func (p localProvider) AnalyzeWhisper(ctx context.Context, kind, body string, identity whisper.IdentityInput) (out moderation.WhisperAssessment, err error) {
	defer finish(p.log, "local_public_manual_review", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return moderation.WhisperAssessment{Body: body, Identity: identity, OrdinaryHit: true, DetectorVersion: "local-manual-v1"}, nil
}
func (p localProvider) ClassifyWhisperCapsule(ctx context.Context, body string) (out moderation.MessageAssessment, err error) {
	defer finish(p.log, "local_private_manual_review", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return moderation.MessageAssessment{OrdinaryHit: true, DetectorVersion: "local-manual-private-v1"}, nil
}
func (p localProvider) Redact(ctx context.Context, kind, body string) (out string, err error) {
	defer finish(p.log, "local_content_reset", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return "原内容已移除，请重新编辑后提交审核。", nil
}
func (p localProvider) Erase(ctx context.Context, in privacy.Intent, objects []privacy.Object) (err error) {
	defer finish(p.log, "local_cleanup", time.Now(), &err)
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
func (p localProvider) NotifyCrisis(ctx context.Context, delivery, caseID string) (err error) {
	defer finish(p.log, "local_notification_unconfigured", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	return command.ErrUnavailable
}

type runtimeCatalog struct{ cfg Settings }

func (c runtimeCatalog) Load(ctx context.Context) (whisper.CatalogSnapshot, error) {
	r, e := loadResources(ctx, c.cfg)
	return r.Catalog, e
}
func loadResources(ctx context.Context, cfg Settings) (ResourcesFile, error) {
	if !cfg.LocalTest {
		return config.LoadWhisperResources(ctx, cfg.CatalogFile)
	}
	if e := ctx.Err(); e != nil {
		return ResourcesFile{}, e
	}
	if cfg.CatalogFile != "" {
		return config.LoadWhisperResources(ctx, cfg.CatalogFile)
	}
	r := ResourcesFile{Version: "local-business-v1", Catalog: whisper.CatalogSnapshot{Grades: whisper.Dictionary{Items: []whisper.Item{}}, Reports: []whisper.Item{}, Help: whisper.HelpDirectory{Status: "unavailable", Items: []whisper.HelpItem{}}}}
	// 联系方式为空时不生成假号码；有配置时仅标记为本地测试关怀来源。
	if strings.TrimSpace(cfg.Contact) != "" {
		r.Care = &moderation.CareResources{Version: "local-contact-v1", Source: "本地测试联系方式，未经外部核验", VerifiedAt: time.Now().Format("2006-01-02"), Hotline24Hours: cfg.Contact, Appointment: cfg.Contact, Notice: "本地测试联系资料，请自行确认服务时间。"}
	}
	return r, nil
}
