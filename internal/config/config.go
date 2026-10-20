package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 保存服务设置及可选中间件配置。
type Config struct {
	Environment string
	Server      struct {
		Address                                    string
		ReadTimeout, WriteTimeout, ShutdownTimeout time.Duration
	}
	Log      struct{ Level string }
	Database struct {
		Driver, DSN      string
		MaxOpen, MaxIdle int
		MaxLifetime      time.Duration
	}
	Redis struct {
		Enabled           bool
		Address, Password string
		DB                int
	}
	RocketMQ struct {
		Enabled     bool
		NameServers []string
		Group       string
	}
}

// Load 加载配置，环境变量优先于 YAML 配置。
func Load(path string) (Config, error) {
	v := viper.New()
	v.SetDefault("environment", "foundation")
	v.SetDefault("server.address", "127.0.0.1:8080")
	v.SetDefault("server.readtimeout", "5s")
	v.SetDefault("server.writetimeout", "10s")
	v.SetDefault("server.shutdowntimeout", "10s")
	v.SetDefault("log.level", "info")
	v.SetDefault("database.driver", "")
	v.SetDefault("database.dsn", "")
	v.SetDefault("database.maxopen", 20)
	v.SetDefault("database.maxidle", 5)
	v.SetDefault("database.maxlifetime", "30m")
	v.SetDefault("redis.enabled", false)
	v.SetDefault("redis.address", "localhost:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	v.SetDefault("rocketmq.enabled", false)
	v.SetDefault("rocketmq.nameservers", []string{"localhost:9876"})
	v.SetDefault("rocketmq.group", "liyuanci-api")
	v.SetEnvPrefix("APP")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("读取配置失败：%w", err)
		}
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return c, fmt.Errorf("解析配置失败：%w", err)
	}
	if c.Database.Driver != "" && c.Database.Driver != "postgres" {
		return c, fmt.Errorf("不支持的数据库驱动%q", c.Database.Driver)
	}
	if c.Database.Driver != "" && c.Database.DSN == "" {
		return c, fmt.Errorf("必须配置数据库DSN")
	}
	if c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 || c.Server.ShutdownTimeout <= 0 {
		return c, fmt.Errorf("服务超时必须为正数")
	}
	if c.Database.MaxOpen <= 0 || c.Database.MaxIdle < 0 || c.Database.MaxIdle > c.Database.MaxOpen || c.Database.MaxLifetime <= 0 {
		return c, fmt.Errorf("数据库连接池配置无效")
	}
	if c.RocketMQ.Enabled && (len(c.RocketMQ.NameServers) == 0 || c.RocketMQ.Group == "") {
		return c, fmt.Errorf("必须配置RocketMQ的NameServer和组名")
	}
	// 真实环境不能静默降级到无中间件或Mock。
	if c.Environment != "foundation" && c.Environment != "integration" && c.Environment != "production" {
		return c, fmt.Errorf("不支持的运行环境")
	}
	if c.Environment != "foundation" && (c.Database.Driver != "postgres" || !c.Redis.Enabled || !c.RocketMQ.Enabled) {
		return c, fmt.Errorf("基础环境以外必须启用全部中间件")
	}
	return c, nil
}
