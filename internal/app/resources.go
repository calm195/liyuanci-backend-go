package app

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// provider 是宿主消费的供应商能力；实现不依赖app，宿主拥有其关闭责任。
type provider interface {
	Ready(context.Context) error
	Close()
	AnalyzeWhisper(context.Context, string, string, whisper.IdentityInput) (moderation.WhisperAssessment, error)
	ClassifyWhisperCapsule(context.Context, string) (moderation.MessageAssessment, error)
	Redact(context.Context, string, string) (string, error)
}

type recoveryJournal interface {
	privacy.Journal
	AllIntents(context.Context, string, int) ([]privacy.Intent, error)
}

// runtimeResources 拥有本次装配的业务池、网关和加密账本；不拥有调用方传入的Redis。
// API与各类worker分别创建资源，不在进程间共享可变客户端。
type runtimeResources struct {
	closeOnce sync.Once
	closeErr  error
	cfg       RuntimeSettings
	log       *zap.Logger
	gateway   provider
	codec     *securedata.Codec
	journal   recoveryJournal
	closes    []func() error
	database  *gorm.DB
}

func finish(log *zap.Logger, op string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	log.Info(
		"应用运行操作完成",
		zap.String("operation", op),
		zap.String("result", result),
		zap.Duration("duration", time.Since(start)),
	)
}

// close 必须在HTTP请求或扫描协程结束后调用，逆序释放全部资源，即使其中一项失败。
// 失败清理与宿主停止可重复调用；释放只执行一次，并保留首次关闭结果。
func (r *runtimeResources) close() (err error) {
	defer finish(r.log, "resources_close", time.Now(), &err)
	r.closeOnce.Do(func() {
		for i := len(r.closes) - 1; i >= 0; i-- {
			if r.closes[i]() != nil {
				r.closeErr = command.ErrUnavailable
			}
		}
		if r.gateway != nil {
			r.gateway.Close()
		}
	})
	return r.closeErr
}

// pool 在当前宿主内复用唯一业务池；领域授权仍在每次业务调用中执行。
func (r *runtimeResources) pool(ctx context.Context) (*gorm.DB, error) {
	if r.database != nil {
		return r.database, nil
	}
	db, closePool, err := platform.OpenRuntimeDatabaseWithLimits(
		ctx,
		r.cfg.DatabaseDSN,
		r.log,
		r.cfg.DatabaseMaxOpen,
		r.cfg.DatabaseMaxIdle,
		r.cfg.DatabaseMaxLifetime,
	)
	if err != nil {
		return nil, err
	}
	r.database = db
	r.closes = append(r.closes, closePool)
	return db, nil
}

func openRuntimeResources(ctx context.Context, cfg RuntimeSettings, log *zap.Logger) (out *runtimeResources, err error) {
	if log == nil || len(cfg.WrapKey) != 32 || len(cfg.RecoveryWrapKey) != 32 {
		return nil, command.ErrInvalid
	}
	defer finish(log, "resources_open", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	r := &runtimeResources{cfg: cfg, log: log, gateway: local.WhisperProvider{Logger: log}}
	r.codec, err = securedata.New(local.Keys{Key: cfg.WrapKey, Logger: log}, log)
	if err != nil {
		return nil, err
	}
	recovery, e := securedata.New(local.Keys{Key: cfg.RecoveryWrapKey, Logger: log}, log)
	if e != nil {
		return nil, e
	}
	r.journal, err = privacy.NewFileJournal(cfg.JournalFile, recovery, log)
	if err != nil {
		return nil, err
	}
	log.Info("本机密钥与词库审核依赖装配完成", zap.String("result", "success"))
	return r, nil
}

func (r *runtimeResources) check(ctx context.Context, db *gorm.DB) error {
	var schema bool
	if db.WithContext(ctx).Raw(`SELECT to_regclass('public.resource_feedback') IS NOT NULL AND to_regclass('public.resource_audits') IS NOT NULL AND to_regclass('public.interaction_block_policy') IS NOT NULL AND to_regclass('public.whisper_capsules') IS NOT NULL AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.whisper_notification_refs') AND attname='suppressed' AND NOT attisdropped) AND to_regprocedure('public.clear_held_whisper_comment(bytea,uuid,jsonb)') IS NOT NULL`).
		Scan(&schema).
		Error != nil ||
		!schema {
		return command.ErrUnavailable
	}
	enabled, e := blockpolicy.Enabled(ctx, db)
	if e != nil {
		return e
	}
	if r.cfg.Enabled && !enabled {
		return command.ErrUnavailable
	}

	return nil
}

func (r *runtimeResources) rules(ctx context.Context, db *gorm.DB) error {
	var count int64
	// head代次用于并发控制，快照版本用于资源状态；发布后两者无需相等。
	// 准入只接受三类生效指针实际引用的已发布快照，不接受草案或归档版本。
	if e := db.WithContext(ctx).Raw("SELECT count(*) FROM public.wordlist_heads h JOIN public.wordlist_versions v ON v.id=h.effective_id AND v.type=h.kind WHERE h.kind IN ('crisis','school','ordinary_violation') AND v.status='published'").
		Scan(&count).
		Error; e != nil || count != 3 {
		return command.ErrUnavailable
	}
	return nil
}

func (r *runtimeResources) privacyConfig() privacy.Config {
	return privacy.Config{
		Command:    command.Config(r.cfg.Command),
		Key:        r.cfg.PrivacyKey,
		ReceiptTTL: r.cfg.Command.ReceiptTTL,
		RetryDelay: r.cfg.Tasks.ScanInterval,
		Redactor:   r.gateway,
	}
}
