package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// TestServerCleanupResult 验证正常停止和启动失败都保留关闭错误，且不会中断后续清理或泄露客户端错误。
func TestServerCleanupResult(t *testing.T) {
	original := errors.New("原始服务失败")
	for _, initial := range []error{nil, original} {
		for _, failed := range []bool{false, true} {
			core, logs := observer.New(zap.InfoLevel)
			log := zap.New(core)
			var order []string
			result := func() (err error) {
				err = initial
				defer closeServerResource(log, "shared_resources_close", func() error {
					order = append(order, "shared")
					return nil
				}, &err)
				defer closeServerResource(log, "business_api_close", func() error {
					order = append(order, "business")
					if failed {
						return errors.New("synthetic-private-credentials")
					}
					return nil
				}, &err)
				return err
			}()
			if !slices.Equal(order, []string{"business", "shared"}) {
				t.Fatalf("清理顺序错误：%v", order)
			}
			if initial != nil && !errors.Is(result, original) {
				t.Fatal("原始运行失败丢失")
			}
			if errors.Is(result, command.ErrUnavailable) != failed {
				t.Fatalf("清理失败未传播至退出结果：%v", result)
			}
			if result != nil && strings.Contains(result.Error(), "synthetic-private") {
				t.Fatal("退出错误泄露底层凭据")
			}
			entries := logs.All()
			if len(entries) != 2 {
				t.Fatalf("缺少完成日志：%d", len(entries))
			}
			expected := "success"
			if failed {
				expected = "failure"
			}
			if entries[0].ContextMap()["result"] != expected || entries[1].ContextMap()["result"] != "success" {
				t.Fatal("资源关闭日志未反映独立结果")
			}
			for _, entry := range entries {
				if strings.Contains(entry.Message, "synthetic-private") || entry.ContextMap()["error"] != nil {
					t.Fatal("日志泄露底层错误")
				}
			}
		}
	}
}
