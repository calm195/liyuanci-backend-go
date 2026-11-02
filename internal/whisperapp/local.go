package whisperapp

import (
	"context"
	"strings"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

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
