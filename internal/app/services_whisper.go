package app

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/redis/whisperrate"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/modules/safety"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

// whisperServices 持有树洞与配套治理服务，借用运行资源，不关闭连接或启动扫描。
type whisperServices struct {
	runtime                          *apiRuntime
	service                          *whisper.Service
	reviews, crises, student         *moderation.Service
	deletion, evidence               *privacy.Service
	studentSafety, adminSafety       *safety.Service
	studentResources, adminResources *resourceplaza.Service
}

// buildWhisperServices 构造树洞、治理、隐私和资料服务，并核对启用状态及受控资料。
func buildWhisperServices(ctx context.Context, runtime *apiRuntime) (out *whisperServices, err error) {
	r := runtime.r
	cfg, log := r.cfg, r.log
	api, review, crisis, priv, evidence := runtime.database, runtime.database, runtime.database, runtime.database, runtime.database
	a := &whisperServices{runtime: runtime}
	limiter, e := whisperrate.New(runtime.client, cfg.RateKey, log, time.Second)
	if e != nil {
		return nil, e
	}
	a.service, err = whisper.NewService(api, log, r.codec, runtimeCatalog{cfg}, limiter, moderation.WhisperReviewSink{Logger: log}, whisper.Config{Enabled: cfg.Enabled, Command: command.Config(cfg.Command), ReceiptKey: cfg.ReceiptKey, CursorKey: cfg.CursorKey})
	if err != nil {
		return nil, err
	}
	if _, err = a.service.Configuration(ctx, ""); err != nil {
		return nil, err
	}
	if cfg.Enabled {
		resources, e := loadResources(ctx, cfg)
		if e != nil || !cfg.LocalTest && !moderation.CareResourcesReady(resources.Care) {
			return nil, command.ErrUnavailable
		}
	}

	for _, spec := range []struct {
		db  *gorm.DB
		out **moderation.Service
	}{{review, &a.reviews}, {crisis, &a.crises}, {api, &a.student}} {
		*spec.out, err = moderation.New(spec.db, log, r.codec, moderation.Config{Command: command.Config(cfg.Command), CursorKey: cfg.GovernanceKey, MaxEntries: 100})
		if err != nil {
			return nil, err
		}
	}
	a.deletion, err = privacy.New(priv, r.journal, r.codec, r.gateway, log, r.privacyConfig())
	if err != nil {
		return nil, err
	}
	pcfg := r.privacyConfig()
	pcfg.Key = cfg.EvidenceKey
	a.evidence, err = privacy.New(evidence, r.journal, r.codec, r.gateway, log, pcfg)
	if err != nil {
		return nil, err
	}
	for _, spec := range []struct {
		db  *gorm.DB
		out **safety.Service
	}{{api, &a.studentSafety}, {review, &a.adminSafety}} {
		*spec.out, err = safety.New(spec.db, log, r.codec, safety.Config{Command: command.Config(cfg.Command), Key: cfg.SafetyKey, MaxTextBytes: 65536})
		if err != nil {
			return nil, err
		}
	}
	a.studentResources, err = resourceplaza.New(api, log, r.codec, resourceplaza.Config{Command: command.Config(cfg.Command), Key: resourceplaza.DeriveKey(cfg.SafetyKey)})
	if err != nil {
		return nil, err
	}
	a.adminResources, err = resourceplaza.New(review, log, r.codec, resourceplaza.Config{Command: command.Config(cfg.Command), Key: resourceplaza.DeriveKey(cfg.SafetyKey)})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ready 复核树洞自身的当前配置，数据库与外部依赖由运行资源先行检查。
func (a *whisperServices) ready(ctx context.Context) error {
	_, err := a.service.Configuration(ctx, "")
	return err
}
