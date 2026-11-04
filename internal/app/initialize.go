package app

import (
	"context"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// RunInitialization 按显式模式初始化，不迁移、不替换历史密钥或重置已有账号。
// 离线检查先完成配置和词库校验，不打开数据库、Redis或写入词库。
func RunInitialization(ctx context.Context, path string, check bool) (err error) {
	log, err := platform.NewLogger("info")
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()
	defer finish(log, "initialize", time.Now(), &err)
	cfg, err := config.Load(path)
	if err != nil || cfg.Bootstrap.Login == "" {
		return command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if cfg.Mode != "business" {
		if cfg.Mode != "development" || (cfg.Environment != "development" && cfg.Environment != "integration") {
			return command.ErrInvalid
		}
		development, err := developmentSettings(cfg)
		if err != nil {
			return err
		}
		if check {
			return nil
		}
		return InitializeDevelopment(ctx, development, log, cfg.Bootstrap.Login)
	}
	business := cfg.Business
	err = config.ValidateBusiness(business, cfg.Environment, "bootstrap")
	if err != nil {
		return err
	}
	var lists map[string][]string
	if cfg.Bootstrap.WordlistsFile != "" {
		if yamlconfig.Read(cfg.Bootstrap.WordlistsFile, &lists) != nil || len(lists) != 3 {
			return command.ErrInvalid
		}
		for _, kind := range []string{"crisis", "school", "ordinary_violation"} {
			if len(lists[kind]) == 0 {
				return command.ErrInvalid
			}
		}
	}
	if check {
		return nil
	}
	// 初始化只借用共享Redis，数据库连接由领域装配拥有并先行关闭。
	cfg.Database.Driver = ""
	resources, err := platform.Open(ctx, cfg)
	if err != nil {
		return command.ErrUnavailable
	}
	defer func() {
		if resources.Close() != nil {
			err = command.ErrUnavailable
		}
	}()
	return InitializeBusiness(ctx, business, cfg.Environment, cfg.Bootstrap.Login, lists, resources.Redis, log)
}
