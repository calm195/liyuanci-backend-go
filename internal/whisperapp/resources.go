package whisperapp

import (
	"context"
	"crypto/rand"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/localtest"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/platform"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

type provider interface {
	ready(context.Context) error
	close()
	AnalyzeWhisper(context.Context, string, string, whisper.IdentityInput) (moderation.WhisperAssessment, error)
	ClassifyWhisperCapsule(context.Context, string) (moderation.MessageAssessment, error)
	Redact(context.Context, string, string) (string, error)
	Erase(context.Context, privacy.Intent, []privacy.Object) error
	NotifyCrisis(context.Context, string, string) error
}
type recoveryJournal interface {
	privacy.Journal
	AllIntents(context.Context, string, int) ([]privacy.Intent, error)
}
type resources struct {
	cfg     Settings
	log     *zap.Logger
	gateway provider
	codec   *securedata.Codec
	journal recoveryJournal
	closes  []func() error
}

func finish(log *zap.Logger, op string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	log.Info("树洞运行操作完成", zap.String("operation", op), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
func (r *resources) close() (err error) {
	defer finish(r.log, "resources_close", time.Now(), &err)
	for i := len(r.closes) - 1; i >= 0; i-- {
		if r.closes[i]() != nil {
			err = command.ErrUnavailable
		}
	}
	r.gateway.close()
	return
}
func (r *resources) pool(ctx context.Context, dsn, role string) (*gorm.DB, error) {
	var db *gorm.DB
	var closePool func() error
	var e error
	if r.cfg.LocalTest {
		db, closePool, e = platform.OpenLocalDatabase(ctx, "integration", r.cfg.DatabaseDSN, r.log)
	} else {
		db, closePool, e = platform.OpenRuntimeDatabase(ctx, dsn, role, r.log)
	}
	if e != nil {
		return nil, e
	}
	r.closes = append(r.closes, closePool)
	return db, nil
}

func openResources(ctx context.Context, cfg Settings, log *zap.Logger) (out *resources, err error) {
	if log == nil || !cfg.LocalTest && (len(cfg.KeyRefs) == 0 || len(cfg.RecoveryKeyRefs) == 0) || cfg.ProviderTimeout <= 0 {
		return nil, command.ErrInvalid
	}
	defer finish(log, "resources_open", time.Now(), &err)
	if cfg.LocalTest {
		r := &resources{cfg: cfg, log: log, gateway: localProvider{log}}
		r.codec, err = securedata.New(localtest.Keys{Key: cfg.WrapKey, Logger: log}, log)
		if err != nil {
			return nil, err
		}
		recovery, e := securedata.New(localtest.Keys{Key: cfg.RecoveryWrapKey, Logger: log}, log)
		if e != nil {
			return nil, e
		}
		r.journal, err = localtest.NewJournal(cfg.JournalFile, recovery, log)
		if err != nil {
			return nil, err
		}
		log.Info("本地树洞依赖装配完成", zap.Bool("manual_review", true), zap.Bool("external_gateway", false), zap.String("result", "success"))
		return r, nil
	}
	g, e := newGateway(cfg, log)
	if e != nil {
		return nil, e
	}
	r := &resources{cfg: cfg, log: log, gateway: g}
	defer func() {
		if err != nil {
			_ = r.close()
		}
	}()
	if err = g.ready(ctx); err != nil {
		return nil, err
	}
	r.codec, err = securedata.New(gatewayKeys{g, cfg.KeyRefs, "business"}, log)
	if err != nil {
		return nil, err
	}
	recovery, e := securedata.New(gatewayKeys{g, cfg.RecoveryKeyRefs, "recovery"}, log)
	if e != nil {
		return nil, e
	}
	// 启动时实际包裹/解包随机探针，核对业务与恢复用途；不持久化、不输出探针或密钥。
	for _, codec := range []*securedata.Codec{r.codec, recovery} {
		probe := make([]byte, 32)
		if _, err = rand.Read(probe); err != nil {
			clear(probe)
			return nil, command.ErrUnavailable
		}
		binding := securedata.Binding{Purpose: "startup_probe", ObjectID: "runtime", Version: 1}
		envelope, e := codec.Encrypt(ctx, binding, probe)
		if e != nil {
			clear(probe)
			return nil, command.ErrUnavailable
		}
		plaintext, e := codec.Decrypt(ctx, binding, envelope)
		equal := string(plaintext) == string(probe)
		clear(plaintext)
		clear(probe)
		if e != nil || !equal {
			return nil, command.ErrUnavailable
		}
	}
	writer, e := r.pool(ctx, cfg.RecoveryWriterDSN, "liyuanci_recovery_writer")
	if e != nil {
		return nil, e
	}
	reader, e := r.pool(ctx, cfg.RecoveryReaderDSN, "liyuanci_recovery_reader")
	if e != nil {
		return nil, e
	}
	r.journal, err = privacy.NewJournal(ctx, writer, reader, recovery, log, cfg.ProviderTimeout)
	if err != nil {
		return nil, err
	}
	return r, nil
}
func (r *resources) check(ctx context.Context, db *gorm.DB) error {
	var schema bool
	if db.WithContext(ctx).Raw(`SELECT to_regclass('public.interaction_block_policy') IS NOT NULL AND to_regclass('public.whisper_capsules') IS NOT NULL AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.whisper_notification_refs') AND attname='suppressed' AND NOT attisdropped) AND to_regprocedure('public.clear_held_whisper_comment(bytea,uuid,jsonb)') IS NOT NULL`).Scan(&schema).Error != nil || !schema {
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

func (r *resources) rules(ctx context.Context, db *gorm.DB) error {
	var count int64
	// head代次用于并发控制，快照版本用于资源状态；发布后两者无需相等。
	// 准入只接受三类生效指针实际引用的已发布快照，不接受草案或归档版本。
	if e := db.WithContext(ctx).Raw("SELECT count(*) FROM public.wordlist_heads h JOIN public.wordlist_versions v ON v.id=h.effective_id AND v.type=h.kind WHERE h.kind IN ('crisis','school','ordinary_violation') AND v.status='published'").Scan(&count).Error; e != nil || count != 3 {
		return command.ErrUnavailable
	}
	return nil
}

// recoveryGate 在读取或外发开启前扫描包含已确认项的独立日志，任何恢复目标仍存在则拒绝启动。
// 由专用隐私进程先恢复屏障并完成重删；普通删除中断后的冷启动也采用同一失败关闭边界。
func (r *resources) recoveryGate(ctx context.Context, db *gorm.DB, scope string) error {
	after := ""
	for {
		intents, e := r.journal.AllIntents(ctx, after, 100)
		if e != nil {
			return e
		}
		for _, in := range intents {
			after = in.ID
			if in.Scope != scope {
				continue
			}
			var n int64
			model := any(&entity.Account{})
			if scope == "evidence" {
				model = &entity.EvidenceCase{}
			}
			if e = db.WithContext(ctx).Model(model).Where("id=?", in.Target).Count(&n).Error; e != nil {
				return command.ErrUnavailable
			}
			if n != 0 {
				return command.ErrUnavailable
			}
		}
		if len(intents) < 100 {
			return nil
		}
	}
}
func (r *resources) privacyConfig() privacy.Config {
	return privacy.Config{Command: r.cfg.Command, Key: r.cfg.PrivacyKey, ReceiptTTL: r.cfg.Command.ReceiptTTL, RetryDelay: r.cfg.Interval, Redactor: r.gateway}
}
