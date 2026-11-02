package app

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/identity"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/verification"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// DevelopmentSettings 从公共配置与私有环境覆盖加载，使用唯一数据库连接；密钥不进入环境或日志。
type DevelopmentSettings config.Development

// LoadDevelopment 拒绝缺失或复用的密钥，不自动生成或重置持久凭据。
func LoadDevelopment(path string) (DevelopmentSettings, error) {
	app, err := config.Load(path)
	if err != nil {
		return DevelopmentSettings{}, err
	}
	cfg := DevelopmentSettings(app.Development)
	return validateDevelopment(cfg)
}

func validateDevelopment(cfg DevelopmentSettings) (DevelopmentSettings, error) {
	if cfg.LocalTest {
		if cfg.DatabaseDSN == "" || !filepath.IsAbs(cfg.JournalFile) {
			return cfg, command.ErrInvalid
		}
	}
	if cfg.DatabaseDSN == "" || cfg.ObjectDirectory == "" || len(cfg.AdminPassword) < 12 {
		return cfg, command.ErrInvalid
	}
	keys := [][]byte{cfg.WrapKey, cfg.LookupKey, cfg.SessionKey, cfg.CardKey, cfg.VerificationKey, cfg.WishKey, cfg.AdminKey}
	if len(cfg.RecoveryWrapKey) > 0 {
		keys = append(keys, cfg.RecoveryWrapKey)
	}
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

func developmentCommandConfig() command.Config {
	return command.Config{
		Timeout:     5 * time.Second,
		LockTimeout: time.Second,
		ReceiptTTL:  24 * time.Hour,
		MaxAttempts: 3,
	}
}

// OpenDevelopment 在监听前完整装配业务，失败逆序释放已开的池；成功后关闭责任转给 HTTP 宿主。
// 开发期限和资源预算只在本入口生效，扫描和学校规则仅在开发环境模拟通过，生产拒绝此入口。
func OpenDevelopment(ctx context.Context, environment string, cfg DevelopmentSettings, client redis.UniversalClient, logger *zap.Logger, ready httpapi.Readiness) (router *gin.Engine, closeAll func() error, err error) {
	if logger == nil {
		return nil, nil, command.ErrInvalid
	}
	defer finishDevelopment(logger, "application_open", time.Now(), &err)
	if (environment != "integration" && environment != "development") || client == nil || ready == nil {
		return nil, nil, command.ErrInvalid
	}
	var closes []func() error
	closeAll = func() (err error) {
		defer finishDevelopment(logger, "application_close", time.Now(), &err)
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
	database, closePool, e := cfg.pool(ctx, logger)
	if e != nil {
		return nil, closeAll, e
	}
	closes = append(closes, closePool)
	keys := &local.IdentityKeys{WrapKey: cfg.WrapKey, LookupKey: cfg.LookupKey, Logger: logger}
	codec, e := securedata.New(keys, logger)
	if e != nil {
		return nil, closeAll, e
	}
	store, e := identity.New(database, codec, keys, logger)
	if e != nil {
		return nil, closeAll, e
	}
	limiter, e := adminauth.NewRedisLimiter(client, time.Minute, 30)
	if e != nil {
		return nil, closeAll, e
	}
	var exchange studentauth.Exchanger = local.Unavailable{Logger: logger}
	if cfg.WeChatAppID != "" {
		exchange, e = studentauth.NewWeChat(&http.Client{Timeout: 5 * time.Second}, cfg.WeChatAppID, cfg.WeChatSecret, logger)
		if e != nil {
			return nil, closeAll, e
		}
	}
	if info, e := os.Stat(cfg.ObjectDirectory); e != nil || !info.IsDir() {
		return nil, closeAll, command.ErrUnavailable
	}
	// 原开发模式缺外部能力时保持未就绪；显式本地模式只检查已装配测试依赖。
	businessReady := func(ctx context.Context) error {
		if err := ready(ctx); err != nil {
			return err
		}
		for _, db := range []*gorm.DB{database} {
			p, _ := db.DB()
			if p.PingContext(ctx) != nil {
				return command.ErrUnavailable
			}
		}
		// 本地模式只声明已装配测试依赖可用，不能据此认定正式供应商已验收。
		if cfg.LocalTest {
			return nil
		}
		return command.ErrUnavailable
	}
	cmd := developmentCommandConfig()
	var moderationConfig *moderation.Config
	if len(cfg.ModerationKey) > 0 {
		moderationConfig = &moderation.Config{Command: cmd, CursorKey: cfg.ModerationKey, MaxEntries: 2000}
	}
	crisisDB := database
	crisisConfig := &moderation.Config{Command: cmd, CursorKey: safety.DeriveKey(append(append([]byte(nil), cfg.WishKey...), []byte("crisis")...)), MaxEntries: 2000}
	deletionJournal, closeJournal, e := openDevelopmentJournal(ctx, cfg, logger)
	if e != nil {
		return nil, closeAll, e
	}
	closes = append(closes, func() error { closeJournal(); return nil })
	privacyDB, evidenceDeletionDB := database, database
	pc := developmentPrivacyConfig(cfg)
	accountCfg, evidenceCfg := &pc, &pc
	router, e = newStudentRouter(StudentDependencies{
		PrivacyDB: privacyDB, EvidenceDeletionDB: evidenceDeletionDB, DeletionJournal: deletionJournal,
		CrisisDB:  crisisDB,
		StudentDB: database,
		ReviewDB:  database,
		WishDB:    database,
		Logger:    logger,
		Ready:     businessReady,
		Exchange:  exchange,
		Identity:  store,
		Limiter:   limiter,
		Codec:     codec,
		Objects:   &local.Objects{Root: cfg.ObjectDirectory, Logger: logger},
		Scanner:   local.Scanner{Logger: logger},
		Rules:     local.SchoolRules{Logger: logger},
	}, StudentConfig{
		Privacy: accountCfg, EvidenceDeletion: evidenceCfg,
		Crisis:     crisisConfig,
		Moderation: moderationConfig,
		Resources:  &resourceplaza.Config{Command: cmd, Key: resourceplaza.DeriveKey(cfg.WishKey)},
		Safety:     &safety.Config{Command: cmd, Key: safety.DeriveKey(cfg.WishKey), MaxTextBytes: 64 << 10},
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
	admin, e := adminauth.New(database, limiter, logger, adminauth.Config{
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

// InitializeDevelopment 仅供显式离线命令配置检索引用和首次后台账号；普通 OpenDevelopment 不调用它。
func InitializeDevelopment(ctx context.Context, cfg DevelopmentSettings, logger *zap.Logger, login string) (err error) {
	defer finishDevelopment(logger, "initialize", time.Now(), &err)
	db, closePool, e := cfg.pool(ctx, logger)
	if e != nil {
		return e
	}
	defer closePool()
	keys := &local.IdentityKeys{WrapKey: cfg.WrapKey, LookupKey: cfg.LookupKey, Logger: logger}
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
	adminDB := db
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
		Command:         developmentCommandConfig(),
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
