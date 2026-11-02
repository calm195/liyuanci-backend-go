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

// TestPackageBoundaries 防止后续修改把HTTP、配置读取、应用装配或显式迁移引入业务包。
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
			// 迁移仅由应用显式装配；业务、HTTP与适配器不能通过导入迁移包获取DDL能力。
			forbidden := target == "migration" && owner != "app" && owner != "migration"
			switch owner {
			case "app":
			case "config":
				forbidden = target != "yamlconfig" && target != "securedata"
			case "migration":
				// 注册清单依赖纯模型，不反向依赖任何业务服务或运行装配。
				forbidden = target != "entity" && target != "migration"
			case "yamlconfig", "securedata", "entity":
				// 叶子包不能反向引入业务，避免配置经共享能力间接依赖整个业务图。
				forbidden = target != owner
			case "platform":
				forbidden = forbidden || target == "app" || target == "httpapi" || target == "adapters"
			case "httpapi":
				forbidden = forbidden || target == "app" || target == "config" || target == "adapters"
			case "adapters":
				forbidden = forbidden || assemblyOnly[target]
			default:
				forbidden = forbidden || assemblyOnly[target] || target == "adapters"
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

// TestCommandBoundaries 约束所有命令只依赖标准库和app，防止配置读取、客户端或领域装配回流入口。
// 不固定命令数量和文件名，新增命令同样受检查；参数、信号及退出状态可以直接使用标准库。
func TestCommandBoundaries(t *testing.T) {
	root := filepath.Join("..", "..", "cmd")
	const assembly = "github.com/calm195/liyuanci-backend-go/internal/app"
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			dependency, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			// 标准库路径首段不含域名；C是cgo入口，不属于命令参数与信号处理依赖。
			first, _, _ := strings.Cut(dependency, "/")
			if dependency != assembly && (strings.Contains(first, ".") || dependency == "C") {
				t.Errorf("%s 直接导入 %s；应用依赖应交由app装配", path, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
