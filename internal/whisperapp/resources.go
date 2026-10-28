package whisperapp

import (
	"context"
	"crypto/rand"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

type resources struct {
	cfg     Settings
	log     *zap.Logger
	gateway *gateway
	codec   *securedata.Codec
	journal *privacy.PostgresJournal
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
	if dsn == "" {
		return nil, command.ErrInvalid
	}
	db, e := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if e != nil {
		return nil, command.ErrUnavailable
	}
	p, e := db.DB()
	if e != nil {
		return nil, command.ErrUnavailable
	}
	p.SetMaxOpenConns(10)
	p.SetMaxIdleConns(2)
	p.SetConnMaxLifetime(30 * time.Minute)
	fail := func() (*gorm.DB, error) { _ = p.Close(); return nil, command.ErrUnavailable }
	if p.PingContext(ctx) != nil {
		return fail()
	}
	var safe bool
	e = db.WithContext(ctx).Raw(`SELECT NOT pg_is_in_recovery() AND current_user=session_user AND NOT r.rolsuper AND NOT r.rolbypassrls AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication
 AND pg_has_role(current_user,?,'member') AND NOT has_schema_privilege(current_user,'public','CREATE')
 AND NOT has_database_privilege(current_user,current_database(),'CREATE')
 AND NOT EXISTS(SELECT 1 FROM pg_roles g WHERE g.rolname<>? AND g.rolname<>current_user AND pg_has_role(current_user,g.oid,'member'))
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','evidence') AND c.relowner=r.oid)
 FROM pg_roles r WHERE r.rolname=current_user`, role, role).Scan(&safe).Error
	if e != nil || !safe {
		return fail()
	}
	r.closes = append(r.closes, p.Close)
	return db, nil
}
func openResources(ctx context.Context, cfg Settings, log *zap.Logger) (out *resources, err error) {
	if log == nil || len(cfg.KeyRefs) == 0 || len(cfg.RecoveryKeyRefs) == 0 || cfg.ProviderTimeout <= 0 {
		return nil, command.ErrInvalid
	}
	defer finish(log, "resources_open", time.Now(), &err)
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
	if e := db.WithContext(ctx).Raw("SELECT count(*) FROM public.wordlist_heads h JOIN public.wordlist_versions v ON v.id=h.effective_id AND v.type=h.kind AND v.version=h.version WHERE h.kind IN ('crisis','school','ordinary_violation') AND v.status='published'").Scan(&count).Error; e != nil || count != 3 {
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
