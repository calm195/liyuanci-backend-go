package transportmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rocketmq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
)

func configuration() Config {
	return Config{
		NameServers:          []string{"127.0.0.1:9876"},
		Topic:                "durable_test",
		EventType:            "content.scan",
		ProducerGroup:        "durable_producer",
		ConsumerGroup:        "durable_consumer",
		Instance:             "durable_unit",
		AllowUnauthenticated: true,
		SendTimeout:          time.Second,
		HandlerTimeout:       time.Second,
		Concurrency:          1,
		MaxReconsume:         3,
		RetryDelayLevel:      1,
	}
}
func uuid(n int) string { return fmt.Sprintf("80000000-0000-0000-0000-%012d", n) }
func event(n int) durable.Event {
	return durable.Event{EventID: uuid(n), EventType: "content.scan", AggregateID: uuid(n + 100), AggregateVersion: 1, CorrelationID: uuid(n + 200), CreatedAt: time.Unix(100, 0).UTC()}
}

type handleFunc func(context.Context, durable.Event) (durable.Consumption, error)

func (f handleFunc) Consume(ctx context.Context, event durable.Event) (durable.Consumption, error) {
	return f(ctx, event)
}

type fakeProducer struct {
	rocketmq.Producer
	send   func(context.Context, ...*primitive.Message) (*primitive.SendResult, error)
	closes int
}

func (f *fakeProducer) SendSync(ctx context.Context, messages ...*primitive.Message) (*primitive.SendResult, error) {
	return f.send(ctx, messages...)
}
func (f *fakeProducer) Shutdown() error { f.closes++; return nil }

type fakeSubscriber struct {
	rocketmq.PushConsumer
	closed atomic.Bool
}

func (f *fakeSubscriber) Shutdown() error { f.closed.Store(true); return nil }

// T007/REQ-006、007、015、017、018、020：限制通道/凭据和六字段载荷，不允许原文穿透。
func TestWireValidation(t *testing.T) {
	c := configuration()
	if !valid(c) {
		t.Fatal("合成隔离参数无效")
	}
	c.AllowUnauthenticated = false
	if valid(c) {
		t.Fatal("缺密钥不可默认无认证")
	}
	c.Credentials.AccessKey = "SYNTHETIC_AK"
	if valid(c) {
		t.Fatal("半套凭据不可用")
	}
	c.Credentials.SecretKey = "SYNTHETIC_SK"
	if !valid(c) {
		t.Fatal("完整凭据应可装配")
	}
	c.Topic = "liyuanci_foundation_probe_v1"
	if valid(c) {
		t.Fatal("业务不可复用探针主题")
	}
	body, _ := json.Marshal(event(1))
	if got, err := decodeEvent(body, "content.scan"); err != nil || got != event(1) {
		t.Fatal("稳定六字段应往返")
	}
	for _, bad := range []string{string(body) + `{}`, strings.Replace(string(body), `"event_id":`, `"body":"SYNTHETIC_BODY","event_id":`, 1), strings.Replace(string(body), `"event_id":`, `"event_id":"`+uuid(9)+`","event_id":`, 1), `null`, `[]`, strings.Repeat("x", 1025)} {
		if _, err := decodeEvent([]byte(bad), "content.scan"); err == nil {
			t.Fatal("未知/重复字段、超长或尾部 JSON 必须拒绝")
		}
	}
}

// T007/REQ-018：只有确定 SendOK 才返回成功，供应商错误和 SDK 诊断均不泄漏。
func TestSendConfirmationAndLogs(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	log := zap.New(core)
	p := &fakeProducer{}
	s := &Sender{client: p, config: configuration(), logger: log}
	for _, status := range []primitive.SendStatus{primitive.SendOK, primitive.SendFlushDiskTimeout} {
		p.send = func(_ context.Context, messages ...*primitive.Message) (*primitive.SendResult, error) {
			if len(messages) != 1 || messages[0].Topic != s.config.Topic || messages[0].GetKeys() != event(1).EventID {
				t.Error("最小唤醒通道错误")
			}
			var fields map[string]any
			json.Unmarshal(messages[0].Body, &fields)
			if len(fields) != 6 {
				t.Error("不能发送自由正文")
			}
			return &primitive.SendResult{Status: status}, nil
		}
		failure := s.Send(context.Background(), event(1))
		if (failure == nil) != (status == primitive.SendOK) {
			t.Fatal("不确定确认不能记成功")
		}
	}
	p.send = func(context.Context, ...*primitive.Message) (*primitive.SendResult, error) {
		return nil, errors.New("SYNTHETIC_SECRET")
	}
	if s.Send(context.Background(), event(1)) != durable.ErrTemporary {
		t.Fatal("发送失败应交由持久重试")
	}
	if s.Close() != nil || s.Close() != nil || p.closes != 1 {
		t.Fatal("关闭必须幂等")
	}
	if s.Send(context.Background(), event(1)) != durable.ErrConfiguration {
		t.Fatal("关闭后不得再发")
	}
	bridge := sdkLog{logger: log}
	bridge.Error("SYNTHETIC_SECRET", map[string]interface{}{"body": "SYNTHETIC_BODY"})
	bridge.Fatal("SYNTHETIC_SECRET", nil)
	for _, entry := range observed.All() {
		if strings.Contains(entry.Message+fmt.Sprint(entry.ContextMap()), "SYNTHETIC_") {
			t.Fatal("诊断或完成日志泄漏")
		}
	}
}

// T007/REQ-006、007、018：ACK 只在持久源处理完成后，失败/panic 返回 NACK，关闭等待在途回调。
func TestAckNackAndLifecycle(t *testing.T) {
	core, observed := observer.New(zap.InfoLevel)
	log := zap.New(core)
	body, _ := json.Marshal(event(1))
	message := &primitive.MessageExt{Message: primitive.Message{Topic: configuration().Topic, Body: body}}
	ctx := primitive.WithConcurrentlyCtx(context.Background(), primitive.NewConsumeConcurrentlyContext())
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &fakeSubscriber{}
	s := &Subscriber{client: client, config: configuration(), logger: log, ctx: life, cancel: cancel}
	for _, tc := range []struct {
		name    string
		handler handleFunc
		want    consumer.ConsumeResult
	}{
		{"persisted", func(context.Context, durable.Event) (durable.Consumption, error) {
			return durable.Consumption{Applied: true}, nil
		}, consumer.ConsumeSuccess},
		{"replayed", func(context.Context, durable.Event) (durable.Consumption, error) {
			return durable.Consumption{Replayed: true}, nil
		}, consumer.ConsumeSuccess},
		{"cancelled", func(context.Context, durable.Event) (durable.Consumption, error) {
			return durable.Consumption{Cancelled: true}, nil
		}, consumer.ConsumeSuccess},
		{"missing", func(context.Context, durable.Event) (durable.Consumption, error) {
			return durable.Consumption{Missing: true}, nil
		}, consumer.ConsumeSuccess},
		{"deferred", func(context.Context, durable.Event) (durable.Consumption, error) {
			return durable.Consumption{Deferred: true}, nil
		}, consumer.ConsumeSuccess},
		{"failure", func(context.Context, durable.Event) (durable.Consumption, error) {
			return durable.Consumption{}, durable.ErrUnavailable
		}, consumer.ConsumeRetryLater},
		{"no_outcome", func(context.Context, durable.Event) (durable.Consumption, error) { return durable.Consumption{}, nil }, consumer.ConsumeRetryLater},
		{"panic", func(context.Context, durable.Event) (durable.Consumption, error) { panic("SYNTHETIC_SECRET") }, consumer.ConsumeRetryLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.handler = tc.handler
			got, failure := s.receive(ctx, message)
			if failure != nil || got != tc.want {
				t.Fatal("确认映射错误")
			}
		})
	}
	if got, _ := s.receive(ctx, &primitive.MessageExt{Message: primitive.Message{Body: []byte(`{"body":"SYNTHETIC_BODY"}`)}}); got != consumer.ConsumeRetryLater {
		t.Fatal("坏载荷应进入有限重试而不是丢弃")
	}
	started := make(chan struct{})
	done := make(chan struct{})
	s.handler = handleFunc(func(ctx context.Context, _ durable.Event) (durable.Consumption, error) {
		close(started)
		<-ctx.Done()
		if client.closed.Load() {
			t.Error("在途回调退出前不能关闭客户端")
		}
		return durable.Consumption{}, ctx.Err()
	})
	go func() { defer close(done); s.receive(ctx, message) }()
	<-started
	if s.Close() != nil {
		t.Fatal("关闭失败")
	}
	<-done
	if !client.closed.Load() {
		t.Fatal("SDK 应关闭")
	}
	if got, _ := s.receive(ctx, message); got != consumer.ConsumeRetryLater {
		t.Fatal("停止后不能接收新工作")
	}
	for _, entry := range observed.FilterMessage("消息确认决策完成").All() {
		fields := entry.ContextMap()
		if fields["duration"] == nil || fields["result"] == nil || fields["reason"] == nil || strings.Contains(fmt.Sprint(fields), "SYNTHETIC_") {
			t.Fatal("完成日志不完整或泄漏")
		}
	}
}

// T007/REQ-018、020：生产者关闭必须等待同步发送，订阅必须拒绝错误主题。
func TestSenderCloseWaitsForSend(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var shutdown atomic.Bool
	p := &fakeProducer{send: func(context.Context, ...*primitive.Message) (*primitive.SendResult, error) {
		close(started)
		<-release
		return &primitive.SendResult{Status: primitive.SendOK}, nil
	}}
	s := &Sender{client: p, config: configuration(), logger: zap.NewNop()}
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		if s.Send(context.Background(), event(1)) != nil {
			t.Error("发送应成功")
		}
	}()
	<-started
	go func() { defer close(done); s.Close(); shutdown.Store(true) }()
	if shutdown.Load() {
		t.Fatal("发送未返回不能关闭")
	}
	close(release)
	group.Wait()
	<-done
	if p.closes != 1 {
		t.Fatal("必须仅关闭一次")
	}
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := &Subscriber{config: configuration(), logger: zap.NewNop(), ctx: life, cancel: cancel, handler: handleFunc(func(context.Context, durable.Event) (durable.Consumption, error) {
		t.Error("错误主题不得调用处理器")
		return durable.Consumption{Applied: true}, nil
	})}
	body, _ := json.Marshal(event(1))
	got, _ := sub.receive(context.Background(), &primitive.MessageExt{Message: primitive.Message{Topic: "other", Body: body}})
	if got != consumer.ConsumeRetryLater {
		t.Fatal("错误主题不得确认")
	}
}
