// business-init 显式初始化，不作为普通启动副作用。
package main

import (
	"context"
	"flag"
	"os"

	"github.com/calm195/liyuanci-backend-go/internal/app"
)

func run() error {
	path := flag.String("config", "", "公共配置旁的私有环境YAML")
	check := flag.Bool("check-config", false, "仅离线校验配置，不连接或初始化")
	flag.Parse()
	return app.RunBusinessInitialization(context.Background(), *path, *check)
}

func main() {
	if run() != nil {
		_, _ = os.Stderr.WriteString("业务初始化未完成，请核对配置、角色、资料及依赖；已有数据保留。\n")
		os.Exit(1)
	}
}
