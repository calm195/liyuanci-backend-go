package app

import (
	"context"

	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

type runtimeCatalog struct{ cfg RuntimeSettings }

// Load 每次读取当前资源快照，版本和可用性由领域投影再次判断。
func (c runtimeCatalog) Load(ctx context.Context) (whisper.CatalogSnapshot, error) {
	r, e := loadResources(ctx, c.cfg)
	return r.Catalog, e
}

func loadResources(ctx context.Context, cfg RuntimeSettings) (ResourcesFile, error) {
	if !cfg.LocalTest {
		return readResources(ctx, cfg.ResourcesFile)
	}
	if e := ctx.Err(); e != nil {
		return ResourcesFile{}, e
	}
	if cfg.ResourcesFile != "" {
		return readResources(ctx, cfg.ResourcesFile)
	}
	r := ResourcesFile{
		Version: "local-business-v1",
		Catalog: whisper.CatalogSnapshot{
			Grades:  whisper.Dictionary{Items: []whisper.Item{}},
			Reports: []whisper.Item{},
			Help:    whisper.HelpDirectory{Status: "unavailable", Items: []whisper.HelpItem{}},
		},
	}
	return r, nil
}
