// dev-init 只解析显式开发配置并报告退出状态。
package main

import (
	"context"
	"flag"
	"os"

	"github.com/calm195/liyuanci-backend-go/internal/app"
)

func main() {
	if run() != nil {
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "", "显式开发应用YAML")
	flag.Parse()
	return app.RunDevelopmentInitialization(context.Background(), *path)
}
