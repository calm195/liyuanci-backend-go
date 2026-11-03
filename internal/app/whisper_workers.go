package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/execution/workrunner"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

// RunWhisperContent 由正式内容入口调用，调度独立审核、解锁及危机补偿；模块关闭不丢弃已持久工作。
func RunWhisperContent(ctx context.Context, path, environment string, log *zap.Logger) (err error) {
	if log == nil {
		return command.ErrInvalid
	}
	defer finish(log, "content_run", time.Now(), &err)
	cfg, e := config.LoadWhispers(ctx, path, environment)
	if e != nil {
		return e
	}
	return runContentSettings(ctx, cfg, environment, log, false)
}

func runContentSettings(ctx context.Context, cfg WhisperSettings, environment string, log *zap.Logger, check bool) (err error) {
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := openRuntimeResources(startup, cfg, log)
	if e != nil {
		return e
	}
	defer func() {
		if r.close() != nil {
			err = command.ErrUnavailable
		}
	}()
	db, e := r.pool(startup)
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
	resources, e := loadResources(startup, cfg)
	if e != nil {
		return e
	}
	if resources.Version == "" || !cfg.LocalTest && resources.Care == nil {
		return command.ErrUnavailable
	}
	scanner, e := moderation.NewWhisperWorker(
		db,
		log,
		r.codec,
		r.gateway,
		r.gateway,
		moderation.WorkerConfig{
			Consumer:        durable.ConsumerConfig(cfg.Worker.Consumer),
			AnalysisTimeout: cfg.Worker.AnalysisTimeout,
			Care:            resources.Care,
		},
	)
	if e != nil {
		return e
	}
	unlock, e := whisper.NewCapsuleUnlockWorker(db, log, durable.ConsumerConfig(cfg.Worker.Consumer))
	if e != nil {
		return e
	}
	if check {
		return nil
	}
	runners := []*workrunner.Worker{}
	defer func() {
		for i := len(runners) - 1; i >= 0; i-- {
			if runners[i].Close() != nil {
				err = command.ErrUnavailable
			}
		}
	}()
	for _, scanner := range []workrunner.Scanner{scanner, unlock} {
		runner, e := workrunner.OpenDatabase(startup, scanner, log, cfg.Interval, cfg.OperationTimeout)
		if e != nil {
			return e
		}
		runners = append(runners, runner)
	}
	log.Info("树洞内容进程启动完成", zap.String("result", "success"), zap.String("resource_version", resources.Version))
	<-ctx.Done()
	return nil
}

// RunWhisperPrivacy 按领域授权恢复全部删除意图；recoverOnly用于开放读取和外发前的显式恢复演练。
// 数据库、账本或脱敏失败均保留processing；完成回执仅覆盖数据库清理。
func RunWhisperPrivacy(ctx context.Context, path, environment, scope string, recoverOnly bool, log *zap.Logger) (err error) {
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
	return runPrivacySettings(ctx, cfg, scope, recoverOnly, log, false)
}

func runPrivacySettings(ctx context.Context, cfg WhisperSettings, scope string, recoverOnly bool, log *zap.Logger, check bool) (err error) {
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := openRuntimeResources(startup, cfg, log)
	if e != nil {
		return e
	}
	defer func() {
		if r.close() != nil {
			err = command.ErrUnavailable
		}
	}()
	db, e := r.pool(startup)
	if e != nil {
		return e
	}
	pcfg := r.privacyConfig()
	if scope == "evidence" {
		pcfg.Key = cfg.EvidenceKey
	}
	service, e := privacy.New(db, r.journal, r.codec, log, pcfg)
	if e != nil {
		return e
	}
	if check {
		return nil
	}
	if recoverOnly {
		bounded, stop := context.WithTimeout(ctx, cfg.OperationTimeout)
		defer stop()
		ctx = bounded
	}
	if err := replayDeletionIntents(ctx, service, scope); err != nil {
		return err
	}
	log.Info(
		"树洞隐私进程启动完成",
		zap.String("scope", scope),
		zap.Bool("recovery_only", recoverOnly),
		zap.String("result", "success"),
	)
	if !recoverOnly {
		return service.Run(ctx, scope, cfg.Interval)
	}
	if err := drainDeletionTasks(ctx, service, db, scope); err != nil {
		return err
	}
	return r.recoveryGate(ctx, db, scope)
}
