package config

import (
	"context"
	"strings"

	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// Resources 保存版本化公共资料及独立关怀内容，不包含主体或密钥。
type Resources struct {
	Version string
	Catalog Catalog
	Care    *CareResources
}

// readResourceYAML 只读取显式公共资源文件，支持取消，不读取应用凭据。
func readResourceYAML(ctx context.Context, path string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := yamlconfig.Read(path, out); err != nil {
		return ErrInvalid
	}
	return ctx.Err()
}

// LoadResources 读取当前版本的公共资源文件，缺失或无版本时失败关闭。
func LoadResources(ctx context.Context, path string) (out Resources, err error) {
	if err = readResourceYAML(ctx, path, &out); err != nil {
		return
	}
	if strings.TrimSpace(out.Version) == "" {
		return out, ErrUnavailable
	}
	return
}
