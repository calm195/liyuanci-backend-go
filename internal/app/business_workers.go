package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/execution/workrunner"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

// CheckBusinessWorkerConfig 只验证当前进程的完整配置和任务预算/凭据，不消费工作。
func CheckBusinessWorkerConfig(c config.Business, environment, kind string) error {
	if e := config.ValidateBusiness(c, environment, kind); e != nil {
		return e
	}
	return nil
}

// workerSessions 禁止正式扫描进程借用学生上传入口，持久任务仍按领域授权。
type workerSessions struct{}

// Current 始终拒绝交互会话，避免工作进程被误接到学生请求链。
func (workerSessions) Current(context.Context, string, string) (studentauth.Me, error) {
	return studentauth.Me{}, command.ErrForbidden
}

type cardScan struct{ *studentcard.Service }

// ProcessNext 将学生卡的全库到期扫描适配为统一任务循环，不指定外部任务ID。
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
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := openRuntimeResources(startup, c.Runtime, log)
	if e != nil {
		return e
	}
	defer func() {
		if r.close() != nil {
			err = command.ErrUnavailable
		}
	}()
	g, ok := r.gateway.(*gateway.Client)
	if !ok {
		return command.ErrInvalid
	}
	b := gateway.NewBusiness(g, c.LookupKeyRefs, c.Card.MaxBytes*2+65536)
	if e = b.ReadyFor(startup, kind); e != nil {
		return e
	}
	db, e := r.pool(startup)
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
	if kind == "card" {
		s, e := studentcard.New(db, log, workerSessions{}, r.codec, b, b, businessCardConfig(c))
		if e != nil {
			return e
		}
		scanner = cardScan{s}
	} else {
		if e = r.rules(startup, db); e != nil {
			return e
		}
		catalog, e := businessResources(startup, c.Runtime)
		if e != nil {
			return e
		}
		scanner, e = moderation.NewWorker(
			db,
			log,
			r.codec,
			b,
			moderation.WorkerConfig{
				Consumer:        durable.ConsumerConfig(c.Runtime.Worker.Consumer),
				AnalysisTimeout: c.Runtime.Worker.AnalysisTimeout,
				Care:            catalog.Care,
				CrisisNotifier:  b,
				MessageAnalyzer: b,
			},
		)
		if e != nil {
			return e
		}
	}
	if check {
		return nil
	}
	runner, e := workrunner.OpenDatabase(startup, scanner, log, c.Runtime.Interval, c.Runtime.OperationTimeout)
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
	log.Info("业务工作进程启动完成", zap.String("kind", kind), zap.String("result", "success"))
	<-ctx.Done()
	return nil
}
