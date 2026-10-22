package studentcard_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/cardworker"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/devapp"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/identity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
)

// committedHandler 在领域事务提交后首次返回失败，要求真实 Broker 重投，模拟 ACK 前中断。
// 不宣称已执行操作系统杀进程；重投必须读取数据库终态而不能再次调用扫描供应商。
type committedHandler struct {
	service *studentcard.Service
	mu      sync.Mutex
	failed  map[string]bool
	results chan chainResult
}
type chainResult struct {
	id  string
	out durable.Consumption
	err error
}

func (h *committedHandler) Consume(ctx context.Context, event durable.Event) (durable.Consumption, error) {
	out, err := h.service.Consume(ctx, event)
	h.results <- chainResult{event.CorrelationID, out, err}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil && out.Applied && !h.failed[event.CorrelationID] {
		h.failed[event.CorrelationID] = true
		return durable.Consumption{}, durable.ErrTemporary
	}
	return out, err
}

// TestBusinessMessageChain 为 T007/REQ-006、007、015、018、020 验证同一真实 PG/Redis/MQ 领域链。
// 微信、密钥、对象与扫描明确采用合成夹具；不改变生产资源或共享契约。
func TestBusinessMessageChain(t *testing.T) {
	dsn, redisAddress, nameserver := os.Getenv("APP_BUSINESS_TEST_DSN"), os.Getenv("APP_BUSINESS_TEST_REDIS"), os.Getenv("APP_BUSINESS_TEST_NAMESERVER")
	if dsn == "" || redisAddress == "" || nameserver == "" {
		t.Skip("需显式隔离PG、Redis及RocketMQ，使用make integration执行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	core, observed := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	open := func(suffix string) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn + suffix, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if err != nil {
			t.Fatal("连接隔离数据库失败")
		}
		pool, _ := db.DB()
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	root := open("")
	if err := migration.Run(ctx, root, logger, "up"); err != nil {
		t.Fatal(err)
	}
	if err := migration.ApplyPermissions(ctx, root, logger); err != nil {
		t.Fatal(err)
	}
	apiDB := open(" options='-c role=liyuanci_identity_maintenance'")
	workerDB := open(" options='-c role=liyuanci_domain_worker'")
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { _ = client.Close() })
	limiter, _ := adminauth.NewRedisLimiter(client, time.Minute, 1000)
	codec, _ := securedata.New(keys{}, logger)
	store, _ := identity.New(apiDB, codec, keys{}, logger)
	if err := store.Configure(ctx, "openid", "synthetic-chain-lookup"); err != nil {
		t.Fatal(err)
	}
	commands := command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}
	sessions, err := studentauth.New(apiDB, logger, exchanger{}, store, limiter, studentauth.Config{StudentCardAssets: true, SessionTTL: time.Hour, ExchangeTTL: time.Minute, SessionKey: bytes.Repeat([]byte{73}, 32), Command: commands})
	if err != nil {
		t.Fatal(err)
	}
	bucket := &objects{data: map[string][]byte{}}
	scan := &scanner{approved: true}
	cfg := studentcard.Config{DecodeSlots: 2, MaxBytes: 4096, MaxPixels: 100, ReceiptKey: bytes.Repeat([]byte{42}, 32), Command: commands, Lease: 10 * time.Second, RetryDelay: 100 * time.Millisecond, MaxAttempts: 3}
	api, err := studentcard.New(apiDB, logger, sessions, codec, bucket, scan, cfg)
	if err != nil {
		t.Fatal(err)
	}
	service, err := studentcard.New(workerDB, logger, sessions, codec, bucket, scan, cfg)
	if err != nil {
		t.Fatal(err)
	}
	unique := "chain_" + uuid.NewString()[:8]
	mq := transportmq.Config{NameServers: []string{nameserver}, Topic: "liyuanci_student_card_scan_v1", EventType: studentcard.Route().EventType, ProducerGroup: unique + "_producer", ConsumerGroup: unique + "_consumer", Instance: unique, AllowUnauthenticated: true, SendTimeout: 3 * time.Second, HandlerTimeout: 10 * time.Second, Concurrency: 2, MaxReconsume: 3, RetryDelayLevel: 1}
	if err := transportmq.ConfigureSDKLogging(logger); err != nil {
		t.Fatal(err)
	}
	sender, err := transportmq.OpenSender(ctx, mq, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sender.Close() != nil {
			t.Error("关闭生产者失败")
		}
	})
	handler := &committedHandler{service: service, failed: map[string]bool{}, results: make(chan chainResult, 64)}
	subscriber, err := transportmq.OpenSubscriber(ctx, mq, handler, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if subscriber.Close() != nil {
			t.Error("关闭订阅者失败")
		}
	})
	relayCfg := durable.Config{DatabaseTimeout: 3 * time.Second, SendTimeout: 3 * time.Second, LeaseDuration: 10 * time.Second, RetryDelay: 100 * time.Millisecond, MaxAttempts: 3}
	relay, err := durable.NewRelay(workerDB, logger, sender, studentcard.Route(), relayCfg)
	if err != nil {
		t.Fatal(err)
	}
	image := picture(t, color.RGBA{R: 180, A: 255})
	upload := func(t *testing.T) (entity.WorkItem, durable.Event) {
		t.Helper()
		code := uuid.NewString()
		login, err := sessions.Login(ctx, code, code, "source", "chain_login")
		if err != nil {
			t.Fatal(err)
		}
		asset, err := api.Upload(ctx, login.Token, code, "chain_upload", "image/png", bytes.NewReader(image))
		if err != nil {
			t.Fatal(err)
		}
		var work entity.WorkItem
		if root.Where("target_id=?", asset.AssetID).Take(&work).Error != nil {
			t.Fatal("未持久化工作")
		}
		var event durable.Event
		if root.Raw(`SELECT event_id,event_type,aggregate_id,aggregate_version,work_id AS correlation_id,created_at FROM public.outbox_events WHERE work_id=?`, work.ID).Scan(&event).Error != nil {
			t.Fatal("未持久化发件箱")
		}
		return work, event
	}
	await := func(t *testing.T, id string, predicate func(chainResult) bool) {
		t.Helper()
		for {
			select {
			case got := <-handler.results:
				if got.id == id && predicate(got) {
					return
				}
			case <-ctx.Done():
				t.Fatal("真实消息处理超时")
			}
		}
	}
	verify := func(t *testing.T, work entity.WorkItem, status string) {
		t.Helper()
		var current entity.WorkItem
		if root.Where("id=?", work.ID).Take(&current).Error != nil || current.Status != status {
			t.Fatalf("工作终态错误：%s", current.Status)
		}
		var count int64
		if root.Model(&entity.ConsumerReceipt{}).Where("consumer_kind=? AND operation_id=?", studentcard.Route().Kind, work.OperationID).Count(&count).Error != nil || count != 1 {
			t.Fatal("领域回执不唯一或未原子提交")
		}
	}
	t.Run("提交后ACK丢失及重复投递", func(t *testing.T) {
		work, event := upload(t)
		if claimed, err := relay.Once(ctx); err != nil || !claimed {
			t.Fatal("实际转发失败", err)
		}
		await(t, work.ID, func(r chainResult) bool { return r.out.Applied && r.err == nil })
		verify(t, work, "completed")
		await(t, work.ID, func(r chainResult) bool { return r.out.Replayed && r.err == nil })
		if err := sender.Send(ctx, event); err != nil {
			t.Fatal(err)
		}
		await(t, work.ID, func(r chainResult) bool { return r.out.Replayed && r.err == nil })
		verify(t, work, "completed")
		scan.mu.Lock()
		calls := scan.calls
		scan.mu.Unlock()
		if calls != 1 {
			t.Fatalf("重复调用扫描：%d", calls)
		}
	})
	t.Run("外部扫描期间撤销资格和晚到", func(t *testing.T) {
		work, event := upload(t)
		var asset entity.Asset
		if root.Where("id=?", work.TargetID).Take(&asset).Error != nil {
			t.Fatal("缺资产")
		}
		scan.mu.Lock()
		scan.hook = func() {
			// 故障夹具模拟未来注销命令已提交；不冒充完整注销业务实现。
			if root.Exec("UPDATE public.accounts SET lifecycle='deleting',auth_epoch=auth_epoch+1 WHERE id=?", asset.OwnerID).Error != nil {
				panic("撤销失败")
			}
		}
		scan.mu.Unlock()
		if claimed, err := relay.Once(ctx); err != nil || !claimed {
			t.Fatal("转发失败", err)
		}
		await(t, work.ID, func(r chainResult) bool { return r.out.Cancelled && r.err == nil })
		scan.mu.Lock()
		scan.hook = nil
		before := scan.calls
		scan.mu.Unlock()
		if err := sender.Send(ctx, event); err != nil {
			t.Fatal(err)
		}
		await(t, work.ID, func(r chainResult) bool { return r.out.Cancelled && r.err == nil })
		verify(t, work, "cancelled")
		if root.Where("id=?", asset.ID).Take(&asset).Error != nil || asset.Status != "deleting" || asset.ScanStatus == "approved" {
			t.Fatal("取消后仍批准资产")
		}
		scan.mu.Lock()
		after := scan.calls
		scan.mu.Unlock()
		if before != after {
			t.Fatal("取消晚到重扫")
		}
	})
	t.Run("供应商失败持久重试", func(t *testing.T) {
		work, _ := upload(t)
		scan.mu.Lock()
		scan.failure = true
		scan.hook = func() { scan.mu.Lock(); scan.failure = false; scan.hook = nil; scan.mu.Unlock() }
		scan.mu.Unlock()
		if claimed, err := relay.Once(ctx); err != nil || !claimed {
			t.Fatal(err)
		}
		await(t, work.ID, func(r chainResult) bool { return r.err != nil })
		await(t, work.ID, func(r chainResult) bool { return r.out.Applied && r.err == nil })
		await(t, work.ID, func(r chainResult) bool { return r.out.Replayed && r.err == nil })
		verify(t, work, "completed")
	})
	// 先停止订阅，再使用完整装配验证无消息情况下数据库补偿及租约恢复。
	if subscriber.Close() != nil {
		t.Fatal("停止订阅失败")
	}
	if sender.Close() != nil {
		t.Fatal("停止生产者失败")
	}
	// 新调度者使用独立客户端实例和组，模拟旧进程已退出后的资源重建。
	mq.Instance += "_recovery"
	mq.ProducerGroup += "_recovery"
	mq.ConsumerGroup += "_recovery"
	t.Run("过期租约和独立调度恢复", func(t *testing.T) {
		work, _ := upload(t)
		interrupted, stop := context.WithCancel(ctx)
		scan.mu.Lock()
		scan.hook = stop
		scan.mu.Unlock()
		if _, err := service.Process(interrupted, work.ID, "chain_interrupted"); err == nil {
			t.Fatal("取消在途操作不得成功")
		}
		scan.mu.Lock()
		scan.hook = nil
		scan.mu.Unlock()
		// 仅加速故障夹具的租约时间，工作和事件身份均保留。
		if root.Exec("UPDATE public.work_items SET lease_until=clock_timestamp()-interval '1 second' WHERE id=? AND status='leased'", work.ID).RowsAffected != 1 {
			t.Fatal("未保留崩溃租约")
		}
		worker, err := cardworker.Open(ctx, workerDB, service, logger, cardworker.Config{MQ: mq, Relay: relayCfg, RelayInterval: 20 * time.Millisecond, ScanInterval: 20 * time.Millisecond, OperationTimeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer worker.Close()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			var current entity.WorkItem
			if root.Where("id=?", work.ID).Take(&current).Error != nil {
				t.Fatal("读取失败")
			}
			if current.Status == "completed" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("补偿恢复超时")
			case <-ticker.C:
			}
		}
		if worker.Close() != nil {
			t.Fatal("调度关闭失败")
		}
		verify(t, work, "completed")
	})
	for _, process := range []struct{ name, binary, ready, config string }{
		{"学生卡消息", "APP_BUSINESS_TEST_WORKER_BINARY", "开发学生卡消息工作进程启动完成", "APP_CARD_MQFILE"},
		{"内容数据库", "APP_CONTENT_TEST_WORKER_BINARY", "开发内容工作进程启动完成", ""},
		{"内容消息", "APP_CONTENT_TEST_WORKER_BINARY", "开发内容工作进程启动完成", "APP_CONTENT_MQFILE"},
	} {
		t.Run("真实工作进程启动和TERM关闭/"+process.name, func(t *testing.T) {
			binary := os.Getenv(process.binary)
			if binary == "" {
				t.Skip("未指定隔离工作进程制品")
			}
			// 独立受限登录只继承领域工作组；不向真实进程注入迁移管理员连接。
			login, password := "chain_worker_"+strings.ReplaceAll(uuid.NewString(), "-", ""), uuid.NewString()
			if root.Exec("CREATE ROLE "+login+" LOGIN PASSWORD '"+password+"' IN ROLE liyuanci_domain_worker").Error != nil {
				t.Fatal("创建隔离工作身份失败")
			}
			defer func() {
				if root.Exec("DROP ROLE "+login).Error != nil {
					t.Error("清理隔离工作身份失败")
				}
			}()
			workerDSN := strings.Replace(dsn, "user=liyuanci ", "user="+login+" ", 1)
			for _, field := range strings.Fields(workerDSN) {
				if strings.HasPrefix(field, "password=") {
					workerDSN = strings.Replace(workerDSN, field, "password="+password, 1)
					break
				}
			}
			directory := t.TempDir()
			settings := devapp.Settings{StudentDSN: "unused", ReviewDSN: "unused", WishDSN: "unused", AdminDSN: "unused", WorkerDSN: workerDSN, AdminPassword: "synthetic-unused-password", ObjectDirectory: directory}
			keys := []*[]byte{&settings.WrapKey, &settings.LookupKey, &settings.SessionKey, &settings.CardKey, &settings.VerificationKey, &settings.WishKey, &settings.AdminKey}
			for i, key := range keys {
				*key = bytes.Repeat([]byte{byte(i + 1)}, 32)
			}
			write := func(name string, value any) string {
				t.Helper()
				path := filepath.Join(directory, name)
				data, err := json.Marshal(value)
				if err != nil || os.WriteFile(path, data, 0600) != nil {
					t.Fatal("写隔离进程配置失败")
				}
				return path
			}
			mq.Instance += "_process"
			mq.ProducerGroup += "_process"
			mq.ConsumerGroup += "_process"
			workerConfig := cardworker.Config{MQ: mq, Relay: relayCfg, RelayInterval: time.Second, ScanInterval: time.Second, OperationTimeout: 10 * time.Second}
			child := exec.CommandContext(ctx, binary)
			child.Env = append(os.Environ(), "APP_ENVIRONMENT=integration", "APP_DEVAPPFILE="+write("business.json", settings), "APP_CONTENT_ANALYZER=unavailable")
			if process.config == "APP_CONTENT_MQFILE" {
				workerConfig.MQ.Topic = "liyuanci_content_review_v1"
				workerConfig.MQ.EventType = "wish.review_requested"
				workerConfig.MQ.HandlerTimeout = 20 * time.Second
				workerConfig.OperationTimeout = 20 * time.Second
			}
			if process.config != "" {
				child.Env = append(child.Env, process.config+"="+write("mq.json", workerConfig))
			}
			output, err := child.StdoutPipe()
			if err != nil {
				t.Fatal("创建进程输出失败")
			}
			if err = child.Start(); err != nil {
				t.Fatal("启动隔离制品失败")
			}
			defer child.Process.Kill()
			started := make(chan struct{}, 1)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				reader := bufio.NewScanner(output)
				for reader.Scan() {
					if strings.Contains(reader.Text(), process.ready) {
						started <- struct{}{}
					}
				}
			}()
			select {
			case <-started:
			case <-readDone:
				_ = child.Wait()
				t.Fatal("工作进程在完成消息装配前退出")
			case <-ctx.Done():
				_ = child.Process.Kill()
				_ = child.Wait()
				t.Fatal("真实进程启动超时")
			}
			if child.Process.Signal(syscall.SIGTERM) != nil {
				t.Fatal("发送停止信号失败")
			}
			if child.Wait() != nil {
				t.Fatal("真实工作进程未正常关闭")
			}
			<-readDone
		})
	}
	if observed.FilterMessage("消息确认决策完成").FilterField(zap.String("result", "nack")).Len() == 0 {
		t.Fatal("未观察到真实NACK")
	}
	// 所有表与字段（包括账本、专用结构）必须保留数据库对象注释。
	var missing int64
	if root.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname IN ('public','evidence') AND c.relkind='r' AND (coalesce(obj_description(c.oid,'pg_class'),'')='' OR coalesce(col_description(c.oid,a.attnum),'')='')`).Scan(&missing).Error != nil || missing != 0 {
		t.Fatal("数据库对象缺中文注释")
	}
	if !t.Failed() {
		t.Log(fmt.Sprintf("真实PG/Redis/MQ学生卡链完成，提交后NACK重投、重复、取消晚到、外部失败和过期租约恢复通过；消息确认日志%d条", observed.FilterMessage("消息确认决策完成").Len()))
	}
}
