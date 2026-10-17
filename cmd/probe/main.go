// T004/REQ-020：真实PG/Redis检查及MQ生产到独立worker的回执验证，不是业务联调。
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
		return errors.New("probe requires integration environment")
	}
	log, err := platform.NewLogger(c.Log.Level)
	if err != nil {
		return errors.New("logger configuration failed")
	}
	defer func() { _ = log.Sync() }()
	start := time.Now()
	result := "failed"
	defer func() {
		log.Info("middleware probe completed", zap.String("task_id", "T004"), zap.String("req", "REQ-020"), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	// T004/REQ-020：MQ制品不可用时独立验证PG/Redis，明确排除MQ结果。
	if *connectionsOnly {
		c.RocketMQ.Enabled = false
	}
	r, err := platform.Open(ctx, c)
	if err != nil {
		return errors.New("probe resources unavailable")
	}
	defer func() {
		if err := r.Close(); err != nil {
			log.Error("probe resources cleanup failed")
		} else {
			log.Info("probe resources cleanup completed")
		}
	}()
	if *connectionsOnly {
		db, err := r.DB.DB()
		if err != nil || db.PingContext(ctx) != nil || r.Redis.Ping(ctx).Err() != nil {
			return errors.New("PG/Redis connection failed")
		}
		result = "pg_redis_passed_mq_not_tested"
		return nil
	}
	if err = r.Ready(ctx); err != nil {
		return errors.New("PG/Redis readiness failed")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return errors.New("probe random failed")
	}
	id := hex.EncodeToString(random[:])
	body, _ := json.Marshal(worker.Event{ProbeID: id})
	// 同一事件两次发送，验证消费者回执唯一；不宣称覆盖业务幂等、取消或崩溃恢复。
	for i := 0; i < 2; i++ {
		if _, err = r.Producer.SendSync(ctx, primitive.NewMessage(worker.Topic, body)); err != nil {
			return errors.New("probe publish failed")
		}
	}
	log.Info("probe publish completed", zap.String("probe_id", id), zap.String("task_id", "T004"))
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var count int64
		if err = r.DB.WithContext(ctx).Raw("SELECT COALESCE(MAX(delivery_count), 0) FROM foundation_probe_receipts WHERE probe_id = ?", id).Scan(&count).Error; err != nil {
			return errors.New("probe receipt query failed; apply explicit integration migration")
		}
		if count >= 2 {
			// T004/REQ-020：仅收到独立worker的数据库回执后记录往返成功证据。
			log.Info("probe receipt verified", zap.String("task_id", "T004"), zap.String("req", "REQ-020"), zap.String("probe_id", id), zap.Int64("delivery_count", count))
			result = "passed"
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("probe receipt timeout")
		case <-tick.C:
		}
	}
}
