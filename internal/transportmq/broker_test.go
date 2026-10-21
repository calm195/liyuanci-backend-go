package transportmq

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
)

// TestBrokerRoundTrip 验证 T007/REQ-006、007、015、017、018、020 的真实运输。
// 处理器是合成内存夹具；持久去重、领域取消与确认前崩溃另由数据库和后续领域测试证明。
func TestBrokerRoundTrip(t *testing.T) {
	address := os.Getenv("APP_MQ_TEST_NAMESERVER")
	if address == "" {
		t.Skip("未配置隔离真实 Broker")
	}
	c := configuration()
	c.NameServers = []string{address}
	c.SendTimeout = 5 * time.Second
	c.HandlerTimeout = 5 * time.Second
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	if err := ConfigureSDKLogging(logger); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	attempts := map[string]int{}
	effects := map[string]int{}
	received := make(chan string, 32)
	handler := handleFunc(func(ctx context.Context, e durable.Event) (durable.Consumption, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts[e.EventID]++
		// 首次失败要求真实 Broker 重投；重复及晚到由合成终态拒绝再次产生效果。
		if e.EventID == event(2).EventID && attempts[e.EventID] == 1 {
			return durable.Consumption{}, durable.ErrTemporary
		}
		if e.EventID == event(3).EventID {
			received <- "cancelled"
			return durable.Consumption{Cancelled: true}, nil
		}
		if effects[e.EventID] > 0 {
			received <- "replayed"
			return durable.Consumption{Replayed: true}, nil
		}
		effects[e.EventID]++
		received <- e.EventID
		return durable.Consumption{Applied: true}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sub, err := OpenSubscriber(ctx, c, handler, logger)
	if err != nil {
		t.Fatal("真实订阅启动失败", err)
	}
	defer sub.Close()
	sender, err := OpenSender(ctx, c, logger)
	if err != nil {
		t.Fatal("真实生产者启动失败", err)
	}
	defer sender.Close()
	for _, n := range []int{1, 1, 2, 3} {
		if err = sender.Send(ctx, event(n)); err != nil {
			t.Fatal("真实发送失败", err)
		}
	}
	want := map[string]bool{event(1).EventID: true, event(2).EventID: true, "replayed": true, "cancelled": true}
	for len(want) > 0 {
		select {
		case got := <-received:
			delete(want, got)
		case <-ctx.Done():
			t.Fatal("真实重投/重复/晚到超时", want)
		}
	}
	mu.Lock()
	if attempts[event(2).EventID] < 2 || effects[event(1).EventID] != 1 || effects[event(2).EventID] != 1 || effects[event(3).EventID] != 0 {
		t.Error("合成处理器效果与运输重投不符")
	}
	mu.Unlock()
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if sender.Send(cancelled, event(4)) != durable.ErrTemporary {
		t.Error("取消发送不得成功")
	}
	if err = sub.Close(); err != nil {
		t.Fatal("真实订阅退出失败", err)
	}
	if err = sender.Close(); err != nil {
		t.Fatal("真实生产者退出失败", err)
	}
	if sender.Send(ctx, event(4)) != durable.ErrConfiguration {
		t.Error("退出后发送必须拒绝")
	}
	if logs.FilterMessage("消息确认决策完成").Len() < 4 {
		t.Fatal("缺确认完成日志")
	}
	t.Log("真实 Broker 发送、订阅、NACK重投、重复/合成取消晚到、取消发送、退出通过；无领域或数据库原子确认声明")
}
