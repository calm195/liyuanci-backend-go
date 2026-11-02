package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/yamlconfig"
)

// RunBusinessInitialization 仅供显式初始化命令调用；离线检查不创建客户端或写入词库。
// 正常初始化借用Redis并拥有本次资源；下层先释放数据库，返回后再关闭Redis与日志。
func RunBusinessInitialization(ctx context.Context, path string, check bool) error {
	c, e := config.Load(path)
	if e != nil || c.BusinessFile == "" {
		return command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	b, e := config.LoadBusiness(ctx, c.BusinessFile, c.Environment, "bootstrap")
	if e != nil {
		return e
	}
	if check {
		return nil
	}
	var lists map[string][]string
	if c.Bootstrap.WordlistsFile != "" {
		if yamlconfig.Read(c.Bootstrap.WordlistsFile, &lists) != nil || lists == nil {
			return command.ErrInvalid
		}
	}
	log, e := platform.NewLogger(c.Log.Level)
	if e != nil {
		return e
	}
	defer func() { _ = log.Sync() }()
	// 初始化只需要共享限流；数据库由应用账号校验入口打开，不使用通用健康连接。
	c.Database.Driver = ""
	r, e := platform.Open(ctx, c)
	if e != nil {
		return command.ErrUnavailable
	}
	defer r.Close()
	e = InitializeBusiness(ctx, b, c.Environment, c.Bootstrap.Login, lists, r.Redis, log)
	result := "success"
	if e != nil {
		result = "failure"
	}
	log.Info("业务初始化入口结束", zap.String("result", result))
	return e
}

// RunDevelopmentInitialization 只允许显式开发初始化，不覆盖历史密钥或后台口令。
func RunDevelopmentInitialization(ctx context.Context, path string) error {
	cfgApp, err := config.Load(path)
	if err != nil {
		return command.ErrInvalid
	}
	logger, err := zap.NewProduction()
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cfg, err := LoadDevelopment(cfgApp.DevAppFile)
	if err == nil && (cfgApp.Environment == "integration" || cfgApp.Environment == "development") && cfgApp.Bootstrap.Login != "" {
		err = InitializeDevelopment(ctx, cfg, logger, cfgApp.Bootstrap.Login)
	} else {
		logger.Error("缺少显式开发初始化配置")
		return command.ErrInvalid
	}
	if err != nil {
		logger.Error("开发业务初始化失败")
		return command.ErrInvalid
	}
	return nil
}
