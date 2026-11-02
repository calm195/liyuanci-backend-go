// business-init 显式初始化正式检索引用、首个后台账号及可选词库，普通启动绝不调用。
package main

import (
	"context"
	"flag"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/whisperapp"
	"github.com/calm195/liyuanci-backend-go/internal/yamlconfig"
)

func run() error {
	path := flag.String("config", "", "公共配置旁的私有环境YAML")
	check := flag.Bool("check-config", false, "仅离线校验配置，不连接或初始化")
	flag.Parse()
	c, e := config.Load(*path)
	if e != nil || c.BusinessFile == "" {
		return command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, e := config.LoadBusiness(ctx, c.BusinessFile, c.Environment, "bootstrap")
	if e != nil {
		return e
	}
	if *check {
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
	// 初始化只需要共享限流；数据库由角色校验入口打开，不使用通用健康连接。
	c.Database.Driver = ""
	r, e := platform.Open(ctx, c)
	if e != nil {
		return command.ErrUnavailable
	}
	defer r.Close()
	e = whisperapp.InitializeBusiness(ctx, b, c.Environment, c.Bootstrap.Login, lists, r.Redis, log)
	result := "success"
	if e != nil {
		result = "failure"
	}
	log.Info("业务初始化入口结束", zap.String("result", result))
	return e
}
func main() {
	if run() != nil {
		_, _ = os.Stderr.WriteString("业务初始化未完成，请核对配置、角色、资料及依赖；已有数据保留。\n")
		os.Exit(1)
	}
}
