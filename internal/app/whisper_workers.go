package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/execution/workrunner"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

func runContentSettings(ctx context.Context, cfg RuntimeSettings, environment string, log *zap.Logger, check bool) (err error) {
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
		if e = checkDeletionRecovery(startup, db, r.journal, scope); e != nil {
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
			Consumer:        durable.ConsumerConfig(cfg.Tasks.Consumer),
			AnalysisTimeout: cfg.Tasks.AnalysisTimeout,
			Care:            resources.Care,
		},
	)
	if e != nil {
		return e
	}
	unlock, e := whisper.NewCapsuleUnlockWorker(db, log, durable.ConsumerConfig(cfg.Tasks.Consumer))
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
		runner, e := workrunner.OpenDatabase(startup, scanner, log, cfg.Tasks.ScanInterval, cfg.Tasks.OperationTimeout)
		if e != nil {
			return e
		}
		runners = append(runners, runner)
	}
	log.Info("树洞内容进程启动完成", zap.String("result", "success"), zap.String("resource_version", resources.Version))
	<-ctx.Done()
	return nil
}

func runPrivacySettings(ctx context.Context, cfg RuntimeSettings, scope string, recoverOnly bool, log *zap.Logger, check bool) (err error) {
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
	if scope == "account" && cfg.Tencent.Enabled {
		objects, failure := openObjectGateway(cfg, studentauth.AvatarMaxBytes, log)
		if failure != nil {
			return failure
		}
		defer objects.Close()
		pcfg.AvatarObjects = objects
		if resourceObjects, ok := objects.(privacy.ResourceObjects); ok {
			pcfg.ResourceObjects = resourceObjects
		}
	}
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
		bounded, stop := context.WithTimeout(ctx, cfg.Tasks.OperationTimeout)
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
		return service.Run(ctx, scope, cfg.Tasks.ScanInterval)
	}
	if err := drainDeletionTasks(ctx, service, db, scope); err != nil {
		return err
	}
	return checkDeletionRecovery(ctx, db, r.journal, scope)
}
