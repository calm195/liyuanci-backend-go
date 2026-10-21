// Package transportmq 将最小持久工作唤醒接入 RocketMQ，数据库仍是业务权威。
package transportmq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sync"
	"time"

	rocketmq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/apache/rocketmq-client-go/v2/rlog"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// Config 的通道、组和技术参数由服务端固定，凭据只从仓库外注入，不接收客户端策略。
type Config struct {
	NameServers          []string
	Topic                string
	EventType            string
	ProducerGroup        string
	ConsumerGroup        string
	Instance             string
	Credentials          primitive.Credentials
	AllowUnauthenticated bool
	SendTimeout          time.Duration
	HandlerTimeout       time.Duration
	Concurrency          int
	MaxReconsume         int32
	RetryDelayLevel      int
}

func token(s string) bool {
	ok, _ := regexp.MatchString(`^[A-Za-z][A-Za-z0-9_-]{0,100}$`, s)
	return ok
}
func valid(c Config) bool {
	for _, s := range []string{c.Topic, c.ProducerGroup, c.ConsumerGroup, c.Instance} {
		if !token(s) {
			return false
		}
	}
	event, _ := regexp.MatchString(`^[a-z][a-z0-9_.]{0,63}$`, c.EventType)
	keys := c.Credentials.AccessKey != "" && c.Credentials.SecretKey != ""
	partial := (c.Credentials.AccessKey != "") != (c.Credentials.SecretKey != "")
	return event && c.Topic != "liyuanci_foundation_probe_v1" && c.Topic != "TBW102" && len(c.NameServers) > 0 && c.SendTimeout > 0 && c.HandlerTimeout > 0 && c.Concurrency >= 1 && c.Concurrency <= 128 && c.MaxReconsume >= 1 && c.MaxReconsume <= 100 && c.RetryDelayLevel >= 1 && c.RetryDelayLevel <= 18 && !partial && (keys || c.AllowUnauthenticated) && (keys || c.Credentials.SecurityToken == "")
}

// ConfigureSDKLogging 由进程装配方在任何消息客户端启动前调用一次，禁止运行中替换 SDK 全局日志器。
// SDK 仅提供进程级日志接口；丢弃其自由文本/字段，防止内部错误携带原始消息或鉴权信息。
func ConfigureSDKLogging(logger *zap.Logger) error {
	if logger == nil {
		return durable.ErrInvalid
	}
	rlog.SetLogger(sdkLog{logger})
	return nil
}

type sdkLog struct{ logger *zap.Logger }

func (l sdkLog) Debug(string, map[string]interface{}) {
	l.logger.Debug("消息客户端诊断", zap.String("level", "debug"))
}
func (l sdkLog) Info(string, map[string]interface{}) {
	l.logger.Info("消息客户端诊断", zap.String("level", "info"))
}
func (l sdkLog) Warning(string, map[string]interface{}) {
	l.logger.Warn("消息客户端诊断", zap.String("level", "warning"))
}
func (l sdkLog) Error(string, map[string]interface{}) {
	l.logger.Error("消息客户端诊断", zap.String("level", "error"))
}
func (l sdkLog) Fatal(string, map[string]interface{}) {
	l.logger.Error("消息客户端诊断", zap.String("level", "fatal"))
}
func (l sdkLog) Level(string)            {}
func (l sdkLog) OutputPath(string) error { return durable.ErrConfiguration }

// Sender 拥有一个独立生产者；关闭前等待正在执行的同步发送，数据库池由装配方拥有。
type Sender struct {
	client   rocketmq.Producer
	config   Config
	logger   *zap.Logger
	mu       sync.RWMutex
	closed   bool
	closeErr error
}

// OpenSender 显式启动生产职责；启动返回不代表主题/权限已可用，实际发送仍要求 Broker 确认。
func OpenSender(ctx context.Context, c Config, logger *zap.Logger) (out *Sender, err error) {
	if logger == nil {
		return nil, durable.ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("消息生产者启动完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if ctx.Err() != nil {
		return nil, durable.ErrConfiguration
	}
	if !valid(c) {
		return nil, durable.ErrInvalid
	}
	servers, failure := platform.ResolveNameServers(ctx, c.NameServers)
	if failure != nil {
		return nil, durable.ErrConfiguration
	}
	client, failure := rocketmq.NewProducer(producer.WithNameServer(primitive.NamesrvAddr(servers)), producer.WithGroupName(c.ProducerGroup), producer.WithInstanceName(c.Instance+"-producer"), producer.WithCredentials(c.Credentials), producer.WithSendMsgTimeout(c.SendTimeout), producer.WithRetry(0))
	if failure != nil {
		return nil, durable.ErrConfiguration
	}
	if failure = client.Start(); failure != nil {
		_ = client.Shutdown()
		return nil, durable.ErrConfiguration
	}
	return &Sender{client: client, config: c, logger: logger}, nil
}

// Send 仅发送六个技术字段；不确定发送结果按临时失败处理，允许重复但不提前确认发件箱。
func (s *Sender) Send(ctx context.Context, event durable.Event) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		s.logger.Info("消息唤醒发送完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if !validEvent(event, s.config.EventType) {
		return durable.ErrInvalid
	}
	body, failure := json.Marshal(event)
	if failure != nil {
		return durable.ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return durable.ErrConfiguration
	}
	bounded, cancel := context.WithTimeout(ctx, s.config.SendTimeout)
	defer cancel()
	message := primitive.NewMessage(s.config.Topic, body)
	message.WithKeys([]string{event.EventID})
	result, failure := s.client.SendSync(bounded, message)
	if failure != nil || result == nil || result.Status != primitive.SendOK || bounded.Err() != nil {
		return durable.ErrTemporary
	}
	return nil
}

// Close 幂等停止生产者，等待已开始的同步发送返回后才释放 SDK 资源。
func (s *Sender) Close() (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		s.logger.Info("消息生产者关闭完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		if s.client.Shutdown() != nil {
			s.closeErr = durable.ErrUnavailable
		}
	}
	return s.closeErr
}

// Handler 处理当前数据库持久源；返回失败必须 NACK，回执与业务原子提交由实现负责。
type Handler interface {
	Consume(context.Context, durable.Event) (durable.Consumption, error)
}

// Subscriber 拥有一个独立订阅职责，先停止接收并等待在途处理，再关闭 SDK，最后才可由外层关闭 DB。
type Subscriber struct {
	client   rocketmq.PushConsumer
	config   Config
	handler  Handler
	logger   *zap.Logger
	mu       sync.Mutex
	stopping bool
	inflight sync.WaitGroup
	once     sync.Once
	closeErr error
	ctx      context.Context
	cancel   context.CancelFunc
}

// OpenSubscriber 订阅固定通道与事件类型，使用集群消费和单消息批次，禁止复用探针主题。
func OpenSubscriber(ctx context.Context, c Config, handler Handler, logger *zap.Logger) (out *Subscriber, err error) {
	if logger == nil {
		return nil, durable.ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		logger.Info("消息订阅启动完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if ctx.Err() != nil {
		return nil, durable.ErrConfiguration
	}
	if !valid(c) || handler == nil {
		return nil, durable.ErrInvalid
	}
	servers, failure := platform.ResolveNameServers(ctx, c.NameServers)
	if failure != nil {
		return nil, durable.ErrConfiguration
	}
	client, failure := rocketmq.NewPushConsumer(consumer.WithNameServer(primitive.NamesrvAddr(servers)), consumer.WithGroupName(c.ConsumerGroup), consumer.WithInstance(c.Instance+"-consumer"), consumer.WithCredentials(c.Credentials), consumer.WithConsumerModel(consumer.Clustering), consumer.WithConsumeFromWhere(consumer.ConsumeFromFirstOffset), consumer.WithConsumeMessageBatchMaxSize(1), consumer.WithConsumeGoroutineNums(c.Concurrency), consumer.WithMaxReconsumeTimes(c.MaxReconsume), consumer.WithConsumeTimeout(c.HandlerTimeout))
	if failure != nil {
		return nil, durable.ErrConfiguration
	}
	// ctx 只用于启动检查；短启动 context 结束不能取消整个订阅生命周期。
	lifecycle, cancel := context.WithCancel(context.Background())
	sub := &Subscriber{client: client, config: c, handler: handler, logger: logger, ctx: lifecycle, cancel: cancel}
	if failure = client.Subscribe(c.Topic, consumer.MessageSelector{}, sub.receive); failure != nil {
		cancel()
		return nil, durable.ErrConfiguration
	}
	if failure = client.Start(); failure != nil {
		cancel()
		_ = client.Shutdown()
		return nil, durable.ErrConfiguration
	}
	return sub, nil
}

func (s *Subscriber) receive(ctx context.Context, messages ...*primitive.MessageExt) (result consumer.ConsumeResult, err error) {
	start := time.Now()
	result = consumer.ConsumeRetryLater
	reason := "handler_failed"
	reconsumes := int32(0)
	defer func() {
		if recover() != nil {
			result = consumer.ConsumeRetryLater
			err = nil
			reason = "handler_panic"
		}
		decision := "nack"
		if result == consumer.ConsumeSuccess {
			decision = "ack"
		}
		s.logger.Info("消息确认决策完成", zap.String("result", decision), zap.String("reason", reason), zap.Int32("reconsume_times", reconsumes), zap.Duration("duration", time.Since(start)))
	}()
	if concurrent, ok := primitive.GetConcurrentlyCtx(ctx); ok {
		concurrent.DelayLevelWhenNextConsume = s.config.RetryDelayLevel
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		reason = "stopping"
		return result, nil
	}
	s.inflight.Add(1)
	s.mu.Unlock()
	defer s.inflight.Done()
	if len(messages) != 1 || messages[0] == nil {
		reason = "invalid_batch"
		return result, nil
	}
	message := messages[0]
	// 即使 SDK 误投也不跨固定通道执行；主题由服务端装配，不信任消息正文指定路由。
	if message.Topic != s.config.Topic {
		reason = "invalid_topic"
		return result, nil
	}
	reconsumes = message.ReconsumeTimes
	event, failure := decodeEvent(message.Body, s.config.EventType)
	if failure != nil {
		reason = "invalid_event"
		return result, nil
	}
	bounded, cancel := context.WithTimeout(ctx, s.config.HandlerTimeout)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	outcome, failure := s.handler.Consume(bounded, event)
	if failure != nil || bounded.Err() != nil || !(outcome.Applied || outcome.Replayed || outcome.Cancelled || outcome.Missing || outcome.Deferred) {
		return result, nil
	}
	// ACK 表示唤醒已按持久源处理；Deferred 由数据库补偿/待办继续推进，不表示业务完成。
	reason = "persisted"
	if outcome.Missing {
		reason = "missing"
	}
	if outcome.Deferred {
		reason = "deferred"
	}
	if outcome.Replayed {
		reason = "replayed"
	}
	if outcome.Cancelled {
		reason = "cancelled"
	}
	return consumer.ConsumeSuccess, nil
}

// Close 幂等停止新处理、取消在途 context 并等待其退出，然后停止 SDK 和持久化消费位点。
func (s *Subscriber) Close() (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		s.logger.Info("消息订阅关闭完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	s.once.Do(func() {
		s.mu.Lock()
		s.stopping = true
		s.cancel()
		s.mu.Unlock()
		s.inflight.Wait()
		if s.client.Shutdown() != nil {
			s.closeErr = durable.ErrUnavailable
		}
	})
	return s.closeErr
}

func validEvent(event durable.Event, kind string) bool {
	for _, s := range []string{event.EventID, event.AggregateID, event.CorrelationID} {
		ok, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, s)
		if !ok {
			return false
		}
	}
	return event.EventType == kind && event.AggregateVersion > 0 && !event.CreatedAt.IsZero()
}

func decodeEvent(body []byte, kind string) (durable.Event, error) {
	var event durable.Event
	if len(body) == 0 || len(body) > 1024 {
		return event, durable.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return event, durable.ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	allowed := map[string]bool{"event_id": true, "event_type": true, "aggregate_id": true, "aggregate_version": true, "correlation_id": true, "created_at": true}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || !allowed[name] || fields[name] != nil {
			return event, durable.ErrInvalid
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return event, durable.ErrInvalid
		}
		fields[name] = raw
	}
	if _, err = decoder.Token(); err != nil || len(fields) != 6 {
		return event, durable.ErrInvalid
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return event, durable.ErrInvalid
	}
	encoded, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(encoded, &event) != nil || !validEvent(event, kind) {
		return event, durable.ErrInvalid
	}
	return event, nil
}
