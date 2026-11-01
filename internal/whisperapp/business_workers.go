package whisperapp

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

// CheckBusinessWorkerConfig 只验证当前进程的完整配置和MQ事件/预算/凭据，不消费工作。
func CheckBusinessWorkerConfig(c config.Business, environment, kind string) error {
	if e := config.ValidateBusiness(c, environment, kind); e != nil {
		return e
	}
	if kind == "whispers" {
		_, e := workerOptions(c.Runtime, environment)
		return e
	}
	if kind == "card" || kind == "content" {
		_, e := businessWorkerOptions(c, environment, kind)
		return e
	}
	if kind != "account" && kind != "evidence" {
		return command.ErrInvalid
	}
	return nil
}

func businessWorkerOptions(c config.Business, environment, kind string) (*workrunner.Config, error) {
	if c.Runtime.DatabaseTasks() {
		if c.Runtime.DatabasePolling && environment != "integration" {
			return nil, command.ErrForbidden
		}
		return nil, nil
	}
	path, route := c.ContentMQFile, wish.ReviewRoute()
	if kind == "card" {
		path, route = c.CardMQFile, studentcard.Route()
	}
	var options workrunner.Config
	if config.LoadCardWorkerMQ(path, &options) != nil || workrunner.Validate(options, route) != nil || options.OperationTimeout < c.Runtime.OperationTimeout || options.MQ.HandlerTimeout < c.Runtime.OperationTimeout || environment == "production" && options.MQ.AllowUnauthenticated {
		return nil, command.ErrInvalid
	}
	if kind == "card" && (options.OperationTimeout < c.Card.Lease+2*c.Runtime.Command.Timeout+c.Runtime.ProviderTimeout || options.MQ.HandlerTimeout < options.OperationTimeout) {
		return nil, command.ErrInvalid
	}
	return &options, nil
}

type workerSessions struct{}

func (workerSessions) Current(context.Context, string, string) (studentauth.Me, error) {
	return studentauth.Me{}, command.ErrForbidden
}

type cardScan struct{ *studentcard.Service }

func (s cardScan) ProcessNext(ctx context.Context) (durable.Consumption, error) {
	return s.Service.ProcessNext(ctx, "")
}

// RunBusinessWorker 按进程独立装配正式扫描、审核/解锁或隐私任务，配置诊断不启动消费者。
func RunBusinessWorker(ctx context.Context, c config.Business, environment, kind string, check, recoverOnly bool, log *zap.Logger) (err error) {
	if log == nil {
		return command.ErrInvalid
	}
	defer finish(log, "business_worker_"+kind, time.Now(), &err)
	if e := CheckBusinessWorkerConfig(c, environment, kind); e != nil {
		return e
	}
	if recoverOnly && kind != "account" && kind != "evidence" {
		return command.ErrInvalid
	}
	if kind == "whispers" {
		return runContentSettings(ctx, c.Runtime, environment, log, check)
	}
	if kind == "account" || kind == "evidence" {
		return runPrivacySettings(ctx, c.Runtime, kind, recoverOnly, log, check)
	}
	options, e := businessWorkerOptions(c, environment, kind)
	if e != nil {
		return e
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := openResources(startup, c.Runtime, log)
	if e != nil {
		return e
	}
	defer func() {
		if r.close() != nil {
			err = command.ErrUnavailable
		}
	}()
	g, ok := r.gateway.(*gateway)
	if !ok {
		return command.ErrInvalid
	}
	b := &businessGateway{gateway: g, lookupRefs: c.LookupKeyRefs, maxObjectBytes: c.Card.MaxBytes*2 + 65536}
	if e = b.readyBusiness(startup, kind); e != nil {
		return e
	}
	db, e := r.pool(startup, c.Runtime.WorkerDSN, "liyuanci_domain_worker")
	if e != nil {
		return e
	}
	if e = businessSchema(startup, db); e != nil {
		return e
	}
	if e = r.check(startup, db); e != nil {
		return e
	}
	for _, scope := range []string{"account", "evidence"} {
		if e = r.recoveryGate(startup, db, scope); e != nil {
			return e
		}
	}
	var scanner workrunner.Scanner
	route := wish.ReviewRoute()
	if kind == "card" {
		s, e := studentcard.New(db, log, workerSessions{}, r.codec, b, b, businessCardConfig(c))
		if e != nil {
			return e
		}
		scanner, route = cardScan{s}, studentcard.Route()
	} else {
		if e = r.rules(startup, db); e != nil {
			return e
		}
		catalog, e := businessResources(startup, c.Runtime)
		if e != nil {
			return e
		}
		scanner, e = moderation.NewWorker(db, log, r.codec, b, moderation.WorkerConfig{Consumer: c.Runtime.Worker.Consumer, AnalysisTimeout: c.Runtime.Worker.AnalysisTimeout, Care: catalog.Care, CrisisNotifier: b, MessageAnalyzer: b})
		if e != nil {
			return e
		}
	}
	if check {
		return nil
	}
	var runner *workrunner.Worker
	if options == nil {
		runner, e = workrunner.OpenDatabase(startup, scanner, log, c.Runtime.Interval, c.Runtime.OperationTimeout)
	} else {
		if e = transportmq.ConfigureSDKLogging(log); e != nil {
			return e
		}
		runner, e = workrunner.Open(startup, db, scanner, log, *options, route)
	}
	if e != nil {
		return e
	}
	defer func() {
		if runner.Close() != nil {
			err = command.ErrUnavailable
		}
	}()
	stopHeartbeat := platform.StartHeartbeat(ctx, log, "business-"+kind)
	defer stopHeartbeat()
	log.Info("业务工作进程启动完成", zap.String("kind", kind), zap.Bool("mq_enabled", options != nil), zap.String("result", "success"))
	<-ctx.Done()
	return nil
}
