package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type closingProvider struct {
	local.WhisperProvider
	close func()
}

func (p closingProvider) Close() { p.close() }

type borrowedRedis struct {
	redis.UniversalClient
	closed bool
}

func (r *borrowedRedis) Close() error {

	r.closed = true
	return nil
}

// TestAPIOwnership 验证两个入口都遵循拥有/借用边界，失败后仍逆序释放全部资源且重复关闭保持结果。
func TestAPIOwnership(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "成功"
		if fail {
			name = "中途失败"
		}
		t.Run(name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			var order []string
			r := &runtimeResources{log: zap.New(core)}
			r.gateway = closingProvider{close: func() { order = append(order, "gateway") }}
			for _, name := range []string{"database", "http"} {
				r.closes = append(r.closes, func() error {
					order = append(order, name)
					if fail && name == "http" {
						return errors.New("合成关闭失败")
					}
					return nil
				})
			}
			client := &borrowedRedis{}
			runtime := &apiRuntime{r: r, client: client}
			api := &BusinessAPI{runtime: runtime}
			whisper := &WhisperAPI{runtime: runtime}
			// 同一资源即使被多个停止回调触发，也必须只释放一次。
			var wait sync.WaitGroup
			results := make(chan error, 2)
			for _, close := range []func() error{api.Close, whisper.Close} {
				wait.Add(1)
				go func() {

					defer wait.Done()
					results <- close()
				}()
			}
			wait.Wait()
			close(results)
			for err := range results {
				if fail && err != command.ErrUnavailable || !fail && err != nil {
					t.Fatalf("关闭结果不一致：%v", err)
				}
			}
			if !slices.Equal(order, []string{"http", "database", "gateway"}) {
				t.Fatalf("关闭顺序或次数错误：%v", order)
			}
			if client.closed {
				t.Fatal("关闭了借用的Redis")
			}
			entries := logs.FilterMessage("应用运行操作完成").All()
			if len(entries) != 2 {
				t.Fatalf("缺少关闭完成日志：%d", len(entries))
			}
			expected := "success"
			if fail {
				expected = "failure"
			}
			for _, entry := range entries {
				if entry.ContextMap()["result"] != expected {
					t.Fatal("关闭日志丢失失败结果")
				}
			}
		})
	}
}

// TestAPIConstructorFailure 验证资源准入失败不返回可用组件，并保留父操作的失败完成日志。
func TestAPIConstructorFailure(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)
	runtime, services, err := openAPIComponents(context.Background(), WhisperSettings{}, &borrowedRedis{}, log)
	if err != command.ErrInvalid || runtime != nil || services != nil {
		t.Fatal("资源失败返回半套组件", err)
	}
	entries := logs.FilterMessage("应用运行操作完成").All()
	found := false
	for _, entry := range entries {
		fields := entry.ContextMap()
		if fields["operation"] == "api_open" {
			found = true
			if fields["result"] != "failure" {
				t.Fatal("API失败被记录为成功")
			}
		}
	}
	if !found {
		t.Fatal("缺少API失败完成日志")
	}
}
