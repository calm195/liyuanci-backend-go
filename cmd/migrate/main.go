// 显式迁移工具使用 YAML 分项配置生成的唯一连接，不加载开发账号或打印底层驱动错误。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
)

func main() { os.Exit(run()) }

func run() int {
	log, err := zap.NewProduction()
	if err != nil {
		return 1
	}
	defer func() { _ = log.Sync() }()
	start := time.Now()
	result := "failure"
	defer func() {
		log.Info("迁移命令完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	path := flag.String("config", "configs/development.yaml", "与API相同的环境覆盖YAML")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Error("需要显式迁移操作")
		return 1
	}
	action := flag.Arg(0)
	if action != "up" && action != "down" && action != "permissions" && action != "opaque-blocks" {
		log.Error("迁移操作无效")
		return 1
	}
	settings, err := config.Load(*path)
	if err != nil {
		log.Error("应用YAML配置无效")
		return 1
	}
	dsn := settings.Database.DSN
	if dsn == "" {
		log.Error("缺少目标数据库外部配置")
		return 1
	}
	// 屏蔽驱动 SQL 日志，避免连接错误或未来敏感迁移数据泄漏。
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		log.Error("数据库连接初始化失败")
		return 1
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Error("数据库连接获取失败")
		return 1
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			log.Error("迁移连接关闭失败")
		} else {
			log.Info("迁移连接关闭完成", zap.String("result", "success"))
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if action == "opaque-blocks" {
		err = blockpolicy.Enable(ctx, db, log)
	} else if action == "permissions" {
		err = migration.ApplyApplicationPermissions(ctx, db, log)
	} else {
		err = migration.Run(ctx, db, log, action)
		if err == nil && action == "up" {
			err = migration.ApplyApplicationPermissions(ctx, db, log)
		}
	}
	if err != nil {
		log.Error("迁移执行失败，请核对版本、权限和数据约束")
		return 1
	}
	result = "success"
	return 0
}
