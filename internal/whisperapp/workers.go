package whisperapp

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/workrunner"
)

func workerOptions(cfg Settings, environment string) ([]*workrunner.Config, error) {
	if cfg.DatabasePolling {
		if environment != "integration" || cfg.ReviewMQFile != "" || cfg.UnlockMQFile != "" {
			return nil, command.ErrInvalid
		}
		return []*workrunner.Config{nil, nil}, nil
	}
	options := make([]*workrunner.Config, 2)
	for i, spec := range []struct {
		path  string
		route durable.Route
	}{{cfg.ReviewMQFile, moderation.WhisperReviewRoute()}, {cfg.UnlockMQFile, whisper.CapsuleUnlockRoute()}} {
		c := &workrunner.Config{}
		if config.LoadCardWorkerMQ(spec.path, c) != nil || workrunner.Validate(*c, spec.route) != nil || c.OperationTimeout < cfg.OperationTimeout || c.MQ.HandlerTimeout < cfg.OperationTimeout || environment == "production" && c.MQ.AllowUnauthenticated {
			return nil, command.ErrInvalid
		}
		options[i] = c
	}
	if options[0].MQ.Topic == options[1].MQ.Topic || options[0].MQ.ConsumerGroup == options[1].MQ.ConsumerGroup || options[0].MQ.ProducerGroup == options[1].MQ.ProducerGroup {
		return nil, command.ErrInvalid
	}
	return options, nil
}

// RunContent 由正式内容入口调用，调度独立审核、解锁及危机补偿；模块关闭不丢弃已持久工作。
func RunContent(ctx context.Context, path, environment string, log *zap.Logger) (err error) {
	if log == nil {
		return command.ErrInvalid
	}
	defer finish(log, "content_run", time.Now(), &err)
	cfg, e := config.LoadWhispers(ctx, path, environment)
	if e != nil {
		return e
	}
	options, e := workerOptions(cfg, environment)
	if e != nil {
		return e
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := openResources(startup, cfg, log)
	if e != nil {
		return e
	}
	defer func() {
		if r.close() != nil {
			err = command.ErrUnavailable
		}
	}()
	db, e := r.pool(startup, cfg.WorkerDSN, "liyuanci_domain_worker")
	if e != nil {
		return e
	}
	if e = r.check(startup, db); e != nil {
		return e
	}
	if e = r.rules(startup, db); e != nil {
		return e
	}
	for _, scope := range []string{"account", "evidence"} {
		if e = r.recoveryGate(startup, db, scope); e != nil {
			return e
		}
	}
	resources, e := config.LoadWhisperResources(startup, cfg.CatalogFile)
	if e != nil {
		return e
	}
	if resources.Version == "" || resources.Care == nil {
		return command.ErrUnavailable
	}
	scanner, e := moderation.NewWhisperWorker(db, log, r.codec, r.gateway, r.gateway, moderation.WorkerConfig{Consumer: cfg.Worker.Consumer, AnalysisTimeout: cfg.Worker.AnalysisTimeout, Care: resources.Care, CrisisNotifier: r.gateway})
	if e != nil {
		return e
	}
	unlock, e := whisper.NewCapsuleUnlockWorker(db, log, cfg.Worker.Consumer)
	if e != nil {
		return e
	}
	if e = transportmq.ConfigureSDKLogging(log); e != nil {
		return e
	}
	runners := []*workrunner.Worker{}
	defer func() {
		for i := len(runners) - 1; i >= 0; i-- {
			if runners[i].Close() != nil {
				err = command.ErrUnavailable
			}
		}
	}()
	for i, item := range []struct {
		scanner workrunner.Scanner
		route   durable.Route
	}{{scanner, moderation.WhisperReviewRoute()}, {unlock, whisper.CapsuleUnlockRoute()}} {
		var runner *workrunner.Worker
		if options[i] == nil {
			runner, e = workrunner.OpenDatabase(startup, item.scanner, log, cfg.Interval, cfg.OperationTimeout)
		} else {
			runner, e = workrunner.Open(startup, db, item.scanner, log, *options[i], item.route)
		}
		if e != nil {
			return e
		}
		runners = append(runners, runner)
	}
	log.Info("树洞内容进程启动完成", zap.String("result", "success"), zap.Bool("mq_enabled", !cfg.DatabasePolling), zap.String("resource_version", resources.Version))
	<-ctx.Done()
	return nil
}

// RunPrivacy 使用专用身份恢复全部删除意图；recoverOnly用于开放读取和外发前的显式恢复演练。
// 任何供应商失败均保留processing，进程不把缺失供应商替换为成功。
func RunPrivacy(ctx context.Context, path, environment, scope string, recoverOnly bool, log *zap.Logger) (err error) {
	if log == nil {
		return command.ErrInvalid
	}
	defer finish(log, "privacy_run", time.Now(), &err)
	if scope != "account" && scope != "evidence" {
		return command.ErrInvalid
	}
	cfg, e := config.LoadWhispers(ctx, path, environment)
	if e != nil {
		return e
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := openResources(startup, cfg, log)
	if e != nil {
		return e
	}
	defer func() {
		if r.close() != nil {
			err = command.ErrUnavailable
		}
	}()
	dsn, role := cfg.PrivacyDSN, "liyuanci_privacy_maintenance"
	if scope == "evidence" {
		dsn, role = cfg.EvidencePurgerDSN, "liyuanci_evidence_purger"
	}
	db, e := r.pool(startup, dsn, role)
	if e != nil {
		return e
	}
	pcfg := r.privacyConfig()
	if scope == "evidence" {
		pcfg.Key = cfg.EvidenceKey
	}
	service, e := privacy.New(db, r.journal, r.codec, r.gateway, log, pcfg)
	if e != nil {
		return e
	}
	if recoverOnly {
		bounded, stop := context.WithTimeout(ctx, cfg.OperationTimeout)
		defer stop()
		ctx = bounded
	}
	after := ""
	for {
		next, e := service.Recover(ctx, after, 100, scope)
		if e != nil {
			return e
		}
		if next == "" {
			break
		}
		after = next
	}
	log.Info("树洞隐私进程启动完成", zap.String("scope", scope), zap.Bool("recovery_only", recoverOnly), zap.String("result", "success"))
	if !recoverOnly {
		return service.Run(ctx, scope, cfg.Interval)
	}
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		processed, e := service.ProcessNext(ctx, scope)
		if e != nil {
			return e
		}
		if !processed {
			var n int64
			if e = db.WithContext(ctx).Model(&entity.DeletionJob{}).Where("scope=? AND phase<>'completed'", scope).Count(&n).Error; e != nil || n != 0 {
				return command.ErrUnavailable
			}
			return r.recoveryGate(ctx, db, scope)
		}
	}
}
