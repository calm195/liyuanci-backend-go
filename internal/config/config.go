package config

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// Config 是所有进程共用的配置；database 中的独立字段组成唯一数据库连接。
type Config struct {
	Environment, Mode string
	// MasterKey 是唯一可配置的应用主密钥；各领域子密钥只在内存派生。
	MasterKey []byte
	// 以下路径由加载器注入，仅用于内部装配，不能从 YAML 设置。
	DevAppFile   string `json:"-"`
	BusinessFile string `json:"-"`
	WhispersFile string `json:"-"`
	Business     Business
	Development  Development
	Whispers     Whispers
	Worker       struct{ Kind, ContentAnalyzer string }
	Bootstrap    struct{ Login, WordlistsFile string }
	Server       struct {
		Address                                    string
		ReadTimeout, WriteTimeout, ShutdownTimeout time.Duration
	}
	Log      struct{ Level string }
	Database Database
	Redis    struct {
		Enabled                     bool
		Address, Username, Password string
		DB                          int
	}
}

// Load 按 common.yaml、指定环境覆盖 YAML 的顺序深度合并；数组整体替换。
// 所有进程使用相同文件组合；不读取环境变量，不自动选择生产配置，不写回凭据。
func Load(path string) (Config, error) {
	c := Defaults()
	if path == "" {
		path = "configs/development.yaml"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return c, yamlconfig.ErrInvalid
	}
	common := filepath.Join(filepath.Dir(absolute), "common.yaml")
	paths := []string{common}
	if absolute != common {
		paths = append(paths, absolute)
	}
	if err := yamlconfig.ReadLayers(paths, &c); err != nil {
		return c, err
	}
	if c.Environment != "development" && c.Environment != "integration" && c.Environment != "production" {
		return c, fmt.Errorf("环境只允许development或production，integration保留给隔离测试")
	}
	if c.Mode != "foundation" && c.Mode != "development" && c.Mode != "business" && c.Mode != "whispers" {
		return c, fmt.Errorf("运行模式无效")
	}
	if c.Environment == "production" && c.Mode != "business" {
		return c, fmt.Errorf("生产环境必须使用完整业务适配器")
	}
	if err := c.Database.resolve(); err != nil {
		return c, err
	}
	if c.Mode != "foundation" && (c.Database.Driver != "postgres" || c.Database.DSN == "" || !c.Redis.Enabled) {
		return c, fmt.Errorf("业务模式必须配置database连接字段和Redis")
	}
	if c.Server.Address == "" || c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 || c.Server.ShutdownTimeout <= 0 || c.Database.MaxOpen < 1 || c.Database.MaxOpen > 100 || c.Database.MaxIdle < 0 || c.Database.MaxIdle > c.Database.MaxOpen || c.Database.MaxLifetime <= 0 {
		return c, fmt.Errorf("监听、期限或连接池预算无效")
	}
	if c.Environment == "production" && c.Redis.Password == "" {
		return c, fmt.Errorf("生产Redis必须配置鉴权")
	}
	if err := c.resolveKeys(); err != nil {
		return c, err
	}
	// 相对资源/状态路径固定相对于配置目录，与进程工作目录无关。
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Clean(filepath.Join(filepath.Dir(absolute), value))
	}
	c.Bootstrap.WordlistsFile = resolve(c.Bootstrap.WordlistsFile)
	c.Development.DatabaseDSN = c.Database.DSN
	c.Development.DatabaseMaxOpen, c.Development.DatabaseMaxIdle, c.Development.DatabaseMaxLifetime = c.Database.MaxOpen, c.Database.MaxIdle, c.Database.MaxLifetime
	c.Development.JournalFile = resolve(c.Development.JournalFile)
	c.Development.ObjectDirectory = resolve(c.Development.ObjectDirectory)
	c.Development.LocalTest = true
	for _, r := range []*Whispers{&c.Business.Runtime, &c.Whispers} {
		r.DatabaseDSN = c.Database.DSN
		r.DatabaseMaxOpen, r.DatabaseMaxIdle = c.Database.MaxOpen, c.Database.MaxIdle
		r.DatabaseMaxLifetime = c.Database.MaxLifetime
		r.JournalFile, r.CatalogFile = resolve(r.JournalFile), resolve(r.CatalogFile)
	}
	switch c.Mode {
	case "business":
		c.BusinessFile = absolute
	case "development":
		c.DevAppFile = absolute
		if c.Whispers.Enabled {
			c.WhispersFile = absolute
		}
	case "whispers":
		c.WhispersFile = absolute
	}
	return c, nil
}

// Defaults 返回公共基础预算；不包含任何口令、密钥或数据库连接。
func Defaults() Config {
	c := Config{Environment: "development", Mode: "foundation"}
	c.Server.Address = "127.0.0.1:8080"
	c.Server.ReadTimeout, c.Server.WriteTimeout, c.Server.ShutdownTimeout = 5*time.Second, 10*time.Second, 10*time.Second
	c.Log.Level = "info"
	c.Database.Port, c.Database.SSLMode, c.Database.TimeZone = 5432, "disable", "UTC"
	c.Database.ConnectTimeout = 5 * time.Second
	c.Database.MaxOpen, c.Database.MaxIdle, c.Database.MaxLifetime = 10, 2, 30*time.Minute
	c.Redis.Address = "127.0.0.1:6379"
	return c
}
