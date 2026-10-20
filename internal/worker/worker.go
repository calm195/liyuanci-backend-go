// Package worker 实现隔离中间件探针；领域发件箱与可靠业务消费尚未实现。
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	rocketmq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// Topic 限定隔离探针通道，禁止复用未来业务主题。
const Topic = "liyuanci_foundation_probe_v1"

// Event 仅携带随机关联标识，不接受身份、业务正文或凭据。
type Event struct {
	ProbeID string `json:"probe_id"`
}

// Start 启动独立消费职责；只有 PostgreSQL 确认提交才确认消息，重复键只增加技术投递计数，不新增回执。
func Start(c config.Config, r *platform.Resources, log *zap.Logger) (rocketmq.PushConsumer, error) {
	if c.Environment != "integration" || r.DB == nil || r.Redis == nil || r.Producer == nil {
		return nil, errors.New("探针消费者要求隔离集成环境且所有中间件可用")
	}
	client, err := rocketmq.NewPushConsumer(
		consumer.WithNameServer(primitive.NamesrvAddr(r.NameServers)),
		consumer.WithGroupName("liyuanci-foundation-probe-v1"),
		consumer.WithConsumeFromWhere(consumer.ConsumeFromFirstOffset),
		consumer.WithConsumeGoroutineNums(2),
		consumer.WithMaxReconsumeTimes(3),
	)
	if err != nil {
		return nil, errors.New("创建消费者失败")
	}
	err = client.Subscribe(Topic, consumer.MessageSelector{}, func(ctx context.Context, messages ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		for _, m := range messages {
			start := time.Now()
			var event Event
			// 探针标识为32位十六进制；坏消息不进入数据库，有限Broker重试后进入DLQ。
			if err := json.Unmarshal(m.Body, &event); err != nil || !validID(event.ProbeID) {
				log.Error("探针消息消费完成", zap.String("result", "invalid_event"), zap.Duration("duration", time.Since(start)))
				return consumer.ConsumeRetryLater, nil
			}
			result := r.DB.WithContext(ctx).Exec("INSERT INTO foundation_probe_receipts (probe_id) VALUES (?) ON CONFLICT (probe_id) DO UPDATE SET delivery_count = foundation_probe_receipts.delivery_count + 1", event.ProbeID)
			if result.Error != nil {
				log.Error("探针消息消费完成", zap.String("result", "storage_failed"), zap.Duration("duration", time.Since(start)))
				return consumer.ConsumeRetryLater, nil
			}
			log.Info("探针消息消费完成", zap.String("probe_id", event.ProbeID), zap.String("result", "persisted"), zap.Duration("duration", time.Since(start)))
		}
		return consumer.ConsumeSuccess, nil
	})
	if err != nil {
		return nil, fmt.Errorf("订阅探针主题失败：%w", err)
	}
	if err = client.Start(); err != nil {
		_ = client.Shutdown()
		return nil, errors.New("启动消费者失败")
	}
	log.Info("消费者启动完成", zap.String("topic", Topic))
	return client, nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
