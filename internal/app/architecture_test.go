package app

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowedInternalImports 逐包登记生产依赖；新增模块或协作边须在此说明职责。
// 使用完整相对包路径，分类目录不是包，叶子能力不能借父目录规则扩大权限。
var allowedInternalImports = map[string][]string{
	"adapters/gateway": {"execution/command"},
	"adapters/local": {
		"execution/command",
		"modules/moderation",
		"modules/privacy",
		"modules/whisper",
		"modules/wish",
		"platform/securedata",
	},
	"adapters/redis/authrate":     {"execution/command"},
	"adapters/redis/contentcache": {"contentquery"},
	"adapters/redis/whisperrate":  {"execution/command", "modules/whisper"},
	"app": {
		"contentquery",
		"adapters/gateway",
		"adapters/local",
		"adapters/redis/authrate",
		"adapters/redis/contentcache",
		"adapters/redis/whisperrate",
		"config",
		"entity",
		"execution/command",
		"execution/durable",
		"execution/workrunner",
		"httpapi",
		"migration",
		"modules/adminauth",
		"modules/blockpolicy",
		"modules/chat",
		"modules/identity",
		"modules/moderation",
		"modules/moduleguide",
		"modules/privacy",
		"modules/resourceplaza",
		"modules/safety",
		"modules/studentauth",
		"modules/studentcard",
		"modules/verification",
		"modules/whisper",
		"modules/wish",
		"platform",
		"platform/securedata",
		"platform/yamlconfig",
	},
	"contentquery":         {"execution/command"},
	"config":               {"platform/securedata", "platform/yamlconfig"},
	"entity":               {},
	"execution/command":    {"entity"},
	"execution/durable":    {"entity", "execution/command"},
	"execution/workrunner": {"execution/durable"},
	"httpapi": {
		"contentquery",
		"execution/command",
		"modules/adminauth",
		"modules/chat",
		"modules/moderation",
		"modules/moduleguide",
		"modules/privacy",
		"modules/resourceplaza",
		"modules/safety",
		"modules/studentauth",
		"modules/studentcard",
		"modules/verification",
		"modules/whisper",
		"modules/wish",
	},
	"migration": {"entity"},
	// 模块说明独立维护版本与发布快照，复用权威命令授权和实体。
	"modules/moduleguide": {"entity", "execution/command"},
	"modules/adminauth":   {"entity", "execution/command"},
	"modules/blockpolicy": {"execution/command"},
	// 聊天通过独立审核协议发布工作和读取结果，不依赖心愿生命周期实现。
	"modules/chat":          {"entity", "execution/command", "execution/durable", "modules/contentreview", "platform/securedata"},
	"modules/contentreview": {"execution/durable"},
	"modules/identity":      {"entity", "platform/securedata"},
	// 审核复核来源内容，调用处罚效果与树洞通知协作函数。
	"modules/moderation": {
		"entity",
		"execution/command",
		"execution/durable",
		"modules/chat",
		"modules/contentreview",
		"modules/safety",
		"modules/whisper",
		"modules/wish",
		"platform/securedata",
	},
	// 删除与脱敏需复核心愿故事及其隐私屏障。
	"modules/privacy":       {"entity", "execution/command", "modules/wish", "platform/securedata"},
	"modules/resourceplaza": {"entity", "execution/command", "platform/securedata"},
	// 安全治理在同一事务中撤销申请、限制会话和消息。
	"modules/safety": {
		"entity",
		"execution/command",
		"modules/blockpolicy",
		"modules/chat",
		"modules/wish",
		"platform/securedata",
	},
	// 登录复用身份检索和黑名单协议策略。
	"modules/studentauth": {"entity", "execution/command", "modules/blockpolicy", "modules/identity", "platform/imageinput"},
	// 学生卡工作处理复核当前学生会话。
	"modules/studentcard": {"entity", "execution/command", "execution/durable", "modules/studentauth", "platform/imageinput", "platform/securedata"},
	// 资格认证使用统一身份绑定与检索锁。
	"modules/verification": {"entity", "execution/command", "modules/identity", "platform/securedata"},
	// 树洞复核黑名单与安全处罚，避免模块开关绕过限制。
	"modules/whisper": {
		"contentquery",
		"entity",
		"execution/command",
		"execution/durable",
		"modules/blockpolicy",
		"modules/safety",
		"platform/securedata",
	},
	"modules/wish":        {"contentquery", "entity", "execution/command", "execution/durable", "modules/contentreview", "platform/securedata"},
	"platform":            {"config", "execution/command"},
	"platform/imageinput": {},
	"platform/securedata": {},
	"platform/yamlconfig": {},
	// 测试配置与隔离数据库夹具只供测试使用；实体用于日志表映射。
	"testconfig": {"platform/yamlconfig", "entity"},
}

func allowedInternalImport(owner, target string) bool {
	return slices.Contains(allowedInternalImports[owner], target)
}

// TestPackageBoundaries 约束完整包路径，防止业务回引装配、配置、HTTP或迁移。
// 生产包必须显式登记，测试夹具可直接装配依赖且不进入生产白名单。
func TestPackageBoundaries(t *testing.T) {
	root := filepath.Clean("..")
	const prefix = "github.com/calm195/liyuanci-backend-go/internal/"
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
		owner := filepath.ToSlash(filepath.Dir(relative))
		if _, ok := allowedInternalImports[owner]; !ok {
			t.Errorf("%s 的生产包未登记职责与依赖", relative)
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
			if !allowedFrameworkImport(owner, dependency) {
				t.Errorf("%s 直接引入具体客户端或配置读取：%s", relative, dependency)
			}
			if !strings.HasPrefix(dependency, prefix) {
				continue
			}
			target := strings.TrimPrefix(dependency, prefix)
			if !allowedInternalImport(owner, target) {
				t.Errorf("%s 越过包边界导入 %s", relative, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestDependencyPolicy 验证合法协作与同父目录越权，防止归组后把叶子规则放宽为整层权限。
func TestDependencyPolicy(t *testing.T) {
	for _, tc := range []struct {
		owner, target string
		allowed       bool
	}{
		{"config", "platform/yamlconfig", true},
		{"config", "platform/securedata", true},
		{"config", "platform", false},
		{"config", "yamlconfig/unregistered", false},
		{"platform/yamlconfig", "config", false},
		{"entity", "modules/wish", false},
		{"migration", "entity", true},
		{"modules/wish", "migration", false},
		{"modules/wish", "app", false},
		{"modules/wish", "httpapi", false},
		{"modules/wish", "adapters/gateway", false},
		{"modules/wish", "modules/chat", false},
		{"modules/chat", "modules/wish", false},
		{"modules/chat", "modules/contentreview", true},
		{"modules/contentreview", "modules/moderation", false},
		{"execution/durable", "modules/wish", false},
		{"httpapi", "config", false},
		{"app", "testconfig", false},
		{"unregistered", "entity", false},
	} {
		t.Run(tc.owner+"->"+tc.target, func(t *testing.T) {
			if got := allowedInternalImport(tc.owner, tc.target); got != tc.allowed {
				t.Fatalf("允许=%v，期望%v", got, tc.allowed)
			}
		})
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

// allowedFrameworkImport 让领域继续依赖注入接口，Redis/Gin/Viper及直接YAML解析留在所属边界。
func allowedFrameworkImport(owner, dependency string) bool {
	if !strings.HasPrefix(owner, "modules/") {
		return true
	}
	for _, prefix := range []string{
		"github.com/gin-gonic/gin",
		"github.com/redis/go-redis/",
		"github.com/spf13/viper",
		"go.yaml.in/yaml/",
		"gopkg.in/yaml.",
		"github.com/go-yaml/",
		"github.com/goccy/go-yaml",
	} {
		if strings.HasPrefix(dependency, prefix) {
			return false
		}
	}
	return true
}

// TestModuleClientBoundary 防止已归位的Redis适配器或HTTP/配置框架回流业务包。
func TestModuleClientBoundary(t *testing.T) {
	for _, tc := range []struct {
		owner, dependency string
		allowed           bool
	}{
		{"modules/adminauth", "github.com/redis/go-redis/v9", false},
		{"modules/studentauth", "github.com/gin-gonic/gin", false},
		{"modules/wish", "github.com/spf13/viper", false},
		{"modules/whisper", "go.yaml.in/yaml/v3", false},
		{"modules/privacy", "gorm.io/gorm", true},
		{"adapters/redis/authrate", "github.com/redis/go-redis/v9", true},
		{"platform/yamlconfig", "github.com/spf13/viper", true},
	} {
		t.Run(tc.owner+"->"+tc.dependency, func(t *testing.T) {
			if got := allowedFrameworkImport(tc.owner, tc.dependency); got != tc.allowed {
				t.Fatalf("允许=%v，期望%v", got, tc.allowed)
			}
		})
	}
}
