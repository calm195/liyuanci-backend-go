package app

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPackageBoundaries 防止后续修改把HTTP、配置读取或应用装配重新引入业务包。
// 只检查生产源码；测试可以装配夹具，也不会因供应商包的消费者接口依赖被误报。
func TestPackageBoundaries(t *testing.T) {
	root := filepath.Clean("..")
	const prefix = "github.com/calm195/liyuanci-backend-go/internal/"
	assemblyOnly := map[string]bool{"app": true, "httpapi": true, "config": true}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		owner := strings.Split(filepath.ToSlash(relative), "/")[0]
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			dependency, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(dependency, prefix) {
				continue
			}
			target := strings.Split(strings.TrimPrefix(dependency, prefix), "/")[0]
			forbidden := false
			switch owner {
			case "app", "config":
				// 配置自身依赖边界在配置解耦时进一步收紧。
			case "platform":
				forbidden = target == "app" || target == "httpapi" || target == "adapters"
			case "httpapi":
				forbidden = target == "app" || target == "config" || target == "adapters"
			case "adapters":
				forbidden = assemblyOnly[target]
			default:
				forbidden = assemblyOnly[target] || target == "adapters"
			}
			if forbidden {
				t.Errorf("%s 越过包边界导入 %s", relative, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
