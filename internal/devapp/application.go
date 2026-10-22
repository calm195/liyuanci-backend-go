package devapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/cardworker"
	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/identity"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentapi"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/verification"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Settings 只从忽略目录内或显式外部只读文件加载；每个运行池使用独立登录身份，密钥不进入环境或日志。
type Settings struct {
	// ModerationKey 缺省时不启用新增审核路由，由显式初始化补齐独立用途密钥。
	ModerationKey []byte
	// CardMQ 由进程入口显式加载，不接受业务凭据文件隐式启用消息模式。
	CardMQ                                                                      *cardworker.Config `json:"-"`
	StudentDSN, ReviewDSN, WishDSN, AdminDSN, WorkerDSN                         string
	WrapKey, LookupKey, SessionKey, CardKey, VerificationKey, WishKey, AdminKey []byte
	AdminPassword, WeChatAppID, WeChatSecret                                    string
	ObjectDirectory                                                             string
}

// Load 拒绝缺失或复用的密钥，不自动生成或重置持久凭据。
func Load(path string) (Settings, error) {
	var cfg Settings
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, command.ErrUnavailable
	}
	defer clear(data)
	if json.Unmarshal(data, &cfg) != nil || cfg.StudentDSN == "" || cfg.ReviewDSN == "" || cfg.WishDSN == "" || cfg.AdminDSN == "" || cfg.ObjectDirectory == "" || len(cfg.AdminPassword) < 12 {
		return cfg, command.ErrInvalid
	}
	keys := [][]byte{cfg.WrapKey, cfg.LookupKey, cfg.SessionKey, cfg.CardKey, cfg.VerificationKey, cfg.WishKey, cfg.AdminKey}
	if len(cfg.ModerationKey) > 0 {
		keys = append(keys, cfg.ModerationKey)
	}
	for i, key := range keys {
		if len(key) != 32 {
			return cfg, command.ErrInvalid
		}
		for _, prior := range keys[:i] {
			if bytes.Equal(key, prior) {
				return cfg, command.ErrInvalid
			}
		}
	}
	if (cfg.WeChatAppID == "") != (cfg.WeChatSecret == "") {
		return cfg, command.ErrInvalid
	}
	return cfg, nil
}

func commandConfig() command.Config {
	return command.Config{
		Timeout:     5 * time.Second,
		LockTimeout: time.Second,
		ReceiptTTL:  24 * time.Hour,
		MaxAttempts: 3,
	}
}

// pool 为每个模块创建独立连接，校验真实登录用户只属于指定运行组且无 DDL/越权身份。
func pool(ctx context.Context, dsn, role string) (*gorm.DB, func() error, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		return nil, nil, command.ErrUnavailable
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, command.ErrUnavailable
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	fail := func() (*gorm.DB, func() error, error) { sqlDB.Close(); return nil, nil, command.ErrUnavailable }
	if sqlDB.PingContext(ctx) != nil {
		return fail()
	}
	var safe bool
	err = db.WithContext(ctx).Raw(`SELECT current_user=session_user AND NOT r.rolsuper AND NOT r.rolbypassrls AND NOT r.rolcreatedb AND NOT r.rolcreaterole AND NOT r.rolreplication
 AND pg_has_role(current_user, ?, 'member')
 AND NOT has_schema_privilege(current_user,'public','CREATE')
 AND NOT has_database_privilege(current_user,current_database(),'CREATE')
 AND NOT EXISTS (SELECT 1 FROM pg_roles g WHERE g.rolname <> ? AND g.rolname <> current_user AND pg_has_role(current_user,g.oid,'member'))
 AND NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','evidence') AND c.relowner=r.oid)
 FROM pg_roles r WHERE r.rolname=current_user`, role, role).Scan(&safe).Error
	if err != nil || !safe {
		return fail()
	}
	// 运行身份不读取迁移账本；只经系统目录核对本进程必需的结构。
	var schemaReady bool
	if db.WithContext(ctx).Raw(`SELECT to_regclass('public.share_links') IS NOT NULL AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.wishes') AND attname='current_story_version' AND NOT attisdropped) AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.messages') AND attname='peer_epoch' AND NOT attisdropped) AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.notifications') AND attname='version' AND NOT attisdropped) AND to_regclass('public.student_login_exchanges') IS NOT NULL AND to_regclass('public.student_card_payloads') IS NOT NULL AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.wishes') AND attname='draft_deleted_at' AND NOT attisdropped) AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.applications') AND attname='public_message_cipher' AND NOT attisdropped) AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.matches') AND attname='application_id' AND NOT attisdropped) AND to_regclass('public.wordlist_heads') IS NOT NULL AND to_regclass('public.moderation_scans') IS NOT NULL AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.moderation_tasks') AND attname='version' AND NOT attisdropped)`).Scan(&schemaReady).Error != nil || !schemaReady {
		return fail()
	}
	return db, sqlDB.Close, nil
}

// Open 在监听前完整装配业务，失败逆序释放已开的池；成功后关闭责任转给 HTTP 宿主。
// 开发期限和资源预算只在本入口生效，扫描和学校规则仅在开发环境模拟通过，生产拒绝此入口。
func Open(ctx context.Context, environment string, cfg Settings, client redis.UniversalClient, logger *zap.Logger, ready httpapi.Readiness) (router *gin.Engine, closeAll func() error, err error) {
	if logger == nil {
		return nil, nil, command.ErrInvalid
	}
	defer completed(logger, "application_open", time.Now(), &err)
	if environment != "integration" || client == nil || ready == nil {
		return nil, nil, command.ErrInvalid
	}
	var closes []func() error
	closeAll = func() (err error) {
		defer completed(logger, "application_close", time.Now(), &err)
		for i := len(closes) - 1; i >= 0; i-- {
			if closes[i]() != nil {
				err = command.ErrUnavailable
			}
		}
		return
	}
	defer func() {
		if err != nil {
			closeAll()
			closeAll = nil
		}
	}()
	databases := make([]*gorm.DB, 4)
	for i, spec := range []struct{ dsn, role string }{{cfg.StudentDSN, "liyuanci_identity_maintenance"}, {cfg.ReviewDSN, "liyuanci_admin_api"}, {cfg.WishDSN, "liyuanci_api"}, {cfg.AdminDSN, "liyuanci_admin_maintenance"}} {
		db, closePool, e := pool(ctx, spec.dsn, spec.role)
		if e != nil {
			return nil, closeAll, e
		}
		databases[i] = db
		closes = append(closes, closePool)
	}
	keys := &localKeys{wrap: cfg.WrapKey, lookup: cfg.LookupKey, logger: logger}
	codec, e := securedata.New(keys, logger)
	if e != nil {
		return nil, closeAll, e
	}
	store, e := identity.New(databases[0], codec, keys, logger)
	if e != nil {
		return nil, closeAll, e
	}
	limiter, e := adminauth.NewRedisLimiter(client, time.Minute, 30)
	if e != nil {
		return nil, closeAll, e
	}
	var exchange studentauth.Exchanger = unavailable{logger}
	if cfg.WeChatAppID != "" {
		exchange, e = studentauth.NewWeChat(&http.Client{Timeout: 5 * time.Second}, cfg.WeChatAppID, cfg.WeChatSecret, logger)
		if e != nil {
			return nil, closeAll, e
		}
	}
	if info, e := os.Stat(cfg.ObjectDirectory); e != nil || !info.IsDir() {
		return nil, closeAll, command.ErrUnavailable
	}
	// 未配置外部能力时整体 ready 保持失败；路由仍能返回鉴权/校验或可用领域查询。
	businessReady := func(ctx context.Context) error {
		if err := ready(ctx); err != nil {
			return err
		}
		for _, db := range databases {
			p, _ := db.DB()
			if p.PingContext(ctx) != nil {
				return command.ErrUnavailable
			}
		}
		return command.ErrUnavailable
	}
	cmd := commandConfig()
	var moderationConfig *moderation.Config
	if len(cfg.ModerationKey) > 0 {
		moderationConfig = &moderation.Config{Command: cmd, CursorKey: cfg.ModerationKey, MaxEntries: 2000}
	}
	router, e = studentapi.New(studentapi.Dependencies{
		StudentDB: databases[0],
		ReviewDB:  databases[1],
		WishDB:    databases[2],
		Logger:    logger,
		Ready:     businessReady,
		Exchange:  exchange,
		Identity:  store,
		Limiter:   limiter,
		Codec:     codec,
		Objects:   &localObjects{cfg.ObjectDirectory, logger},
		Scanner:   developmentScanner{logger},
		Rules:     developmentSchoolRules{logger},
	}, studentapi.Config{
		Moderation: moderationConfig,
		Auth: studentauth.Config{
			SessionTTL:  24 * time.Hour,
			ExchangeTTL: time.Minute,
			SessionKey:  cfg.SessionKey,
			Command:     cmd,
		},
		Card: studentcard.Config{
			DecodeSlots: 2,
			MaxBytes:    1 << 20,
			MaxPixels:   4_000_000,
			ReceiptKey:  cfg.CardKey,
			Command:     cmd,
			Lease:       30 * time.Second,
			RetryDelay:  time.Minute,
			MaxAttempts: 3,
		},
		Verification: verification.Config{
			Command:      cmd,
			ReceiptKey:   cfg.VerificationKey,
			MaxCardBytes: 1 << 20,
		},
		Chat: &chat.Config{Command: cmd, Key: chat.DeriveKey(cfg.WishKey), MaxBodyBytes: 64 << 10},
		Wish: &wish.ServiceConfig{
			Command:       cmd,
			ReceiptKey:    cfg.WishKey,
			MaxStoryBytes: 64 << 10,
		},
	})
	if e != nil {
		return nil, closeAll, e
	}
	admin, e := adminauth.New(databases[3], limiter, logger, adminauth.Config{
		SessionTTL:      8 * time.Hour,
		InitialPassword: cfg.AdminPassword,
		ReceiptKey:      cfg.AdminKey,
		Command:         cmd,
	})
	if e != nil {
		return nil, closeAll, e
	}
	if e = httpapi.RegisterAdmin(router, admin); e != nil {
		return nil, closeAll, e
	}
	return router, closeAll, nil
}

// Initialize 仅供显式离线命令配置检索引用和首次后台账号；普通 Open 不调用它。
func Initialize(ctx context.Context, cfg Settings, logger *zap.Logger, login string) (err error) {
	defer completed(logger, "initialize", time.Now(), &err)
	db, closePool, e := pool(ctx, cfg.StudentDSN, "liyuanci_identity_maintenance")
	if e != nil {
		return e
	}
	defer closePool()
	keys := &localKeys{cfg.WrapKey, cfg.LookupKey, logger}
	codec, e := securedata.New(keys, logger)
	if e != nil {
		return e
	}
	store, e := identity.New(db, codec, keys, logger)
	if e != nil {
		return e
	}
	for _, purpose := range []string{"openid", "student_number"} {
		if e = store.Configure(ctx, purpose, "local-lookup-v1"); e != nil {
			return e
		}
	}
	adminDB, closeAdmin, e := pool(ctx, cfg.AdminDSN, "liyuanci_admin_maintenance")
	if e != nil {
		return e
	}
	defer closeAdmin()
	// 初始化不执行登录，不需要限流网络访问；依赖仍显式构造，不能放宽实际 HTTP 登录。
	client := redis.NewClient(&redis.Options{Addr: "redis:6379"})
	defer client.Close()
	limiter, e := adminauth.NewRedisLimiter(client, time.Minute, 30)
	if e != nil {
		return e
	}
	admin, e := adminauth.New(adminDB, limiter, logger, adminauth.Config{
		SessionTTL:      8 * time.Hour,
		InitialPassword: cfg.AdminPassword,
		ReceiptKey:      cfg.AdminKey,
		Command:         commandConfig(),
	})
	if e != nil {
		return e
	}
	// 重跑初始化只保留现有账号，不触发 Bootstrap 的拒绝路径或重置口令。
	var count int64
	if e = adminDB.WithContext(ctx).Table("admin_accounts").Count(&count).Error; e != nil {
		return command.ErrUnavailable
	}
	if count > 0 {
		return nil
	}
	_, e = admin.Bootstrap(ctx, login)
	return e
}
