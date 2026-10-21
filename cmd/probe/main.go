// 真实PG/Redis检查及MQ生产到独立worker的回执验证，不是业务联调。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "configs/integration.yaml", "隔离配置路径")
	connectionsOnly := flag.Bool("connections-only", false, "仅验证真实PostgreSQL/Redis，不签MQ通过")
	flag.Parse()
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if c.Environment != "integration" {
		return errors.New("探针仅允许在隔离集成环境运行")
	}
	log, err := platform.NewLogger(c.Log.Level)
	if err != nil {
		return errors.New("日志配置失败")
	}
	defer func() { _ = log.Sync() }()
	start := time.Now()
	result := "failed"
	defer func() {
		log.Info("中间件探针检查完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	// MQ制品不可用时独立验证PG/Redis，明确排除MQ结果。
	if *connectionsOnly {
		c.RocketMQ.Enabled = false
	}
	r, err := platform.Open(ctx, c)
	if err != nil {
		return errors.New("探针依赖资源不可用")
	}
	defer func() {
		if err := r.Close(); err != nil {
			log.Error("探针资源清理失败")
		} else {
			log.Info("探针资源清理完成")
		}
	}()
	if *connectionsOnly {
		db, err := r.DB.DB()
		if err != nil || db.PingContext(ctx) != nil || r.Redis.Ping(ctx).Err() != nil {
			return errors.New("PostgreSQL或Redis连接失败")
		}
		result = "pg_redis_passed_mq_not_tested"
		return nil
	}
	if err = r.Ready(ctx); err != nil {
		return errors.New("PostgreSQL或Redis就绪检查失败")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return errors.New("探针随机标识生成失败")
	}
	id := hex.EncodeToString(random[:])
	body, _ := json.Marshal(worker.Event{ProbeID: id})
	// 同一事件两次发送，验证消费者回执唯一；不宣称覆盖业务幂等、取消或崩溃恢复。
	for i := 0; i < 2; i++ {
		if _, err = r.Producer.SendSync(ctx, primitive.NewMessage(worker.Topic, body)); err != nil {
			return errors.New("探针消息发送失败")
		}
	}
	log.Info("探针消息发送完成", zap.String("probe_id", id))
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var count int64
		if err = r.DB.WithContext(ctx).Model(&entity.FoundationProbeReceipt{}).Select("COALESCE(MAX(delivery_count), 0)").Where("probe_id = ?", id).Scan(&count).Error; err != nil {
			return errors.New("探针回执查询失败，请显式执行集成数据库迁移")
		}
		if count >= 2 {
			// 仅收到独立worker的数据库回执后记录往返成功证据。
			log.Info("探针回执验证成功", zap.String("probe_id", id), zap.Int64("delivery_count", count))
			result = "passed"
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("等待探针回执超时")
		case <-tick.C:
		}
	}
}
