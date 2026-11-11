package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/execution/workrunner"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
)

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
	if e := config.ValidateBusiness(c, environment, kind); e != nil {
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
	cardConfig := businessCardConfig(c)
	if kind == "card" {
		// 上限收紧仅影响新上传，历史待扫图片仍使用原有处理预算。
		cardConfig.MaxBytes = historicalCardMaxBytes
	}
	b, e := openObjectGateway(c.Runtime, cardConfig.MaxBytes*2+65536, log)
	if e != nil {
		return e
	}
	if b != nil {
		defer b.Close()
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
		if e = checkDeletionRecovery(startup, db, r.journal, scope); e != nil {
			return e
		}
	}
	var scanner workrunner.Scanner
	if kind == "card" {
		s, e := studentcard.New(db, log, workerSessions{}, r.codec, objectStore(b), imageScanner(b), cardConfig)
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
			local.Analyzer{Logger: log},
			moderation.WorkerConfig{
				Consumer:        durable.ConsumerConfig(c.Runtime.Tasks.Consumer),
				AnalysisTimeout: c.Runtime.Tasks.AnalysisTimeout,
				Care:            catalog.Care,
				MessageAnalyzer: local.Analyzer{Logger: log},
			},
		)
		if e != nil {
			return e
		}
	}
	if check {
		return nil
	}
	runner, e := workrunner.OpenDatabase(startup, scanner, log, c.Runtime.Tasks.ScanInterval, c.Runtime.Tasks.OperationTimeout)
	if e != nil {
		return e
	}
	defer func() {
		if runner.Close() != nil {
			err = command.ErrUnavailable
		}
	}()
	if kind == "content" {
		auto, e := matchmaking.NewAutoWorker(db, log, r.codec, durable.ConsumerConfig(c.Runtime.Tasks.Consumer))
		if e != nil {
			return e
		}
		autoRunner, e := workrunner.OpenDatabase(startup, auto, log, c.Runtime.Tasks.ScanInterval, max(c.Runtime.Tasks.OperationTimeout, 30*time.Second))
		if e != nil {
			return e
		}
		defer autoRunner.Close()
	}
	stopHeartbeat := platform.StartHeartbeat(ctx, log, "business-"+kind)
	defer stopHeartbeat()
	log.Info("业务工作进程启动完成", zap.String("kind", kind), zap.String("result", "success"))
	<-ctx.Done()
	return nil
}
