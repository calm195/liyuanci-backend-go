package app

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

type runtimeCatalog struct {
	cfg RuntimeSettings
	db  *gorm.DB
}

// Load 每次读取当前资源快照，版本和可用性由领域投影再次判断。
func (c runtimeCatalog) Load(ctx context.Context) (whisper.CatalogSnapshot, error) {
	r, e := loadResources(ctx, c.cfg)
	if e != nil {
		return r.Catalog, e
	}
	// 举报类型以数据库发布状态为权威；资源文件中的旧字段仅为配置兼容保留。
	var rows []entity.ReportType
	if e = c.db.WithContext(ctx).Where("published").Order("code").Find(&rows).Error; e != nil {
		return r.Catalog, e
	}
	r.Catalog.Reports = make([]whisper.Item, 0, len(rows))
	for _, row := range rows {
		r.Catalog.Reports = append(r.Catalog.Reports, whisper.Item{Code: row.Code, Label: row.Label})
	}
	return r.Catalog, nil
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
