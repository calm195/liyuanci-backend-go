package app

import (
	"context"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

// WhisperSettings 是配置边界提供的只读运行约束，所有文件加载由config负责。
type WhisperSettings = config.Whispers

// ResourcesFile 是装配层转换后的资源快照，配置包不依赖领域投影类型。
type ResourcesFile struct {
	Version string
	Catalog whisper.CatalogSnapshot
	Care    *moderation.CareResources
}

// careResources 复制只读资料，避免业务实现和配置结构共享可变指针。
func careResources(value *config.CareResources) *moderation.CareResources {
	if value == nil {
		return nil
	}
	out := moderation.CareResources(*value)
	return &out
}

// readResources 保留版本、可选字段与nil/空数组区别，再由领域校验资料是否可用。
// 转换逐字段执行；不能用JSON往返转换，否则字段标记或数字类型可能悄悄改变。
func readResources(ctx context.Context, path string) (ResourcesFile, error) {
	value, err := config.LoadWhisperResources(ctx, path)
	if err != nil {
		return ResourcesFile{}, err
	}
	return resourceSnapshot(value), nil
}

func resourceSnapshot(value config.WhisperResources) ResourcesFile {
	items := func(values []config.DictionaryItem) []whisper.Item {
		if values == nil {
			return nil
		}
		out := make([]whisper.Item, len(values))
		for i, item := range values {
			out[i] = whisper.Item(item)
		}
		return out
	}
	// 复制可选标量，保证领域侧的修改不会改变原始配置快照。
	copyVersion := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		out := *value
		return &out
	}
	help := whisper.HelpDirectory{Status: value.Catalog.Help.Status, Version: copyVersion(value.Catalog.Help.Version)}
	if value.Catalog.Help.VerifiedAt != nil {
		verified := *value.Catalog.Help.VerifiedAt
		help.VerifiedAt = &verified
	}
	if value.Catalog.Help.Items != nil {
		help.Items = make([]whisper.HelpItem, len(value.Catalog.Help.Items))
		for i, item := range value.Catalog.Help.Items {
			help.Items[i] = whisper.HelpItem(item)
		}
	}
	return ResourcesFile{
		Version: value.Version,
		Care:    careResources(value.Care),
		Catalog: whisper.CatalogSnapshot{
			Grades:  whisper.Dictionary{Version: copyVersion(value.Catalog.Grades.Version), Items: items(value.Catalog.Grades.Items)},
			Reports: items(value.Catalog.Reports),
			Help:    help,
		},
	}
}
