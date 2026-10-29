package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 保存服务设置及可选中间件配置。
type Config struct {
	Environment  string
	DevAppFile   string
	WhispersFile string
	AdminAuth    struct {
		Enabled                                                   bool
		DSN, InitialPasswordFile, ReceiptKeyFile                  string
		SessionTTL, Timeout, LockTimeout, ReceiptTTL, LoginWindow time.Duration
		LoginLimit, MaxAttempts                                   int
	}
	Server struct {
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
	v.SetDefault("devappfile", "")
	v.SetDefault("whispersfile", "")
	v.SetDefault("adminauth.enabled", false)
	v.SetDefault("adminauth.dsn", "")
	v.SetDefault("adminauth.initialpasswordfile", "")
	v.SetDefault("adminauth.receiptkeyfile", "")
	for _, key := range []string{"sessionttl", "timeout", "locktimeout", "receiptttl", "loginwindow"} {
		v.SetDefault("adminauth."+key, "0s")
	}
	v.SetDefault("adminauth.loginlimit", 0)
	v.SetDefault("adminauth.maxattempts", 0)
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
	if c.Database.Driver != "" && c.Database.DSN == "" && c.WhispersFile == "" {
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
	if c.WhispersFile != "" && c.Environment == "foundation" {
		return c, fmt.Errorf("树洞运行配置要求显式业务环境")
	}
	// 显式开发业务使用数据库轮询，允许不启用MQ；生产和完整集成仍要求全部中间件。
	localBusiness := c.Environment == "integration" && (c.DevAppFile != "" || c.WhispersFile != "")
	if c.Environment != "foundation" && (c.Database.Driver != "postgres" || !c.Redis.Enabled || (!c.RocketMQ.Enabled && !localBusiness)) {
		return c, fmt.Errorf("基础环境以外必须启用全部中间件")
	}
	if c.AdminAuth.Enabled && (c.AdminAuth.DSN == "" || c.AdminAuth.InitialPasswordFile == "" || c.AdminAuth.ReceiptKeyFile == "" || !c.Redis.Enabled || c.AdminAuth.SessionTTL <= 0 || c.AdminAuth.Timeout <= 0 || c.AdminAuth.LockTimeout < time.Millisecond || c.AdminAuth.LockTimeout > c.AdminAuth.Timeout || c.AdminAuth.ReceiptTTL <= 0 || c.AdminAuth.LoginWindow < time.Millisecond || c.AdminAuth.LoginLimit < 1 || c.AdminAuth.MaxAttempts < 1 || c.AdminAuth.MaxAttempts > 10) {
		return c, fmt.Errorf("后台身份启用时必须显式配置独立数据库、凭据文件、期限、重试及共享限流")
	}
	if c.DevAppFile != "" && (c.Environment != "integration" || c.AdminAuth.Enabled) {
		return c, fmt.Errorf("本地业务适配器仅允许隔离集成环境且不能重复装配后台身份")
	}
	return c, nil
}
