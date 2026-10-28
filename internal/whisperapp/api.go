package whisperapp

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/platform/whisperrate"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// API 持有本模块连接和供应商资源；宿主先停止HTTP及等待请求，再Close。
type API struct {
	r                          *resources
	client                     redis.UniversalClient
	databases                  []*gorm.DB
	service                    *whisper.Service
	reviews, crises, student   *moderation.Service
	deletion, evidence         *privacy.Service
	studentSafety, adminSafety *safety.Service
}

// OpenAPI 在监听前验证真实权限、资源、密钥、限频、删除日志与恢复门禁，不启动后台任务。
func OpenAPI(ctx context.Context, cfg Settings, client redis.UniversalClient, log *zap.Logger) (out *API, err error) {
	if log == nil || client == nil {
		return nil, command.ErrInvalid
	}
	defer finish(log, "api_open", time.Now(), &err)
	r, e := openResources(ctx, cfg, log)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			_ = r.close()
		}
	}()
	a := &API{r: r, client: client}
	for _, spec := range []struct{ dsn, role string }{{cfg.APIDSN, "liyuanci_api"}, {cfg.ReviewDSN, "liyuanci_admin_api"}, {cfg.CrisisDSN, "liyuanci_crisis_api"}, {cfg.PrivacyDSN, "liyuanci_privacy_maintenance"}, {cfg.EvidenceDeletionDSN, "liyuanci_evidence_delete_api"}} {
		db, e := r.pool(ctx, spec.dsn, spec.role)
		if e != nil {
			return nil, e
		}
		a.databases = append(a.databases, db)
	}
	api, review, crisis, priv, evidence := a.databases[0], a.databases[1], a.databases[2], a.databases[3], a.databases[4]
	if err = r.check(ctx, api); err != nil {
		return nil, err
	}
	if cfg.Enabled {
		if err = r.rules(ctx, review); err != nil {
			return nil, err
		}
	}
	if err = r.recoveryGate(ctx, priv, "account"); err != nil {
		return nil, err
	}
	if err = r.recoveryGate(ctx, evidence, "evidence"); err != nil {
		return nil, err
	}
	if err = client.Ping(ctx).Err(); err != nil {
		return nil, command.ErrUnavailable
	}
	limiter, e := whisperrate.New(client, cfg.RateKey, log, time.Second)
	if e != nil {
		return nil, e
	}
	a.service, err = whisper.NewService(api, log, r.codec, catalogFile{cfg.CatalogFile}, limiter, moderation.WhisperReviewSink{Logger: log}, whisper.Config{Enabled: cfg.Enabled, Command: cfg.Command, ReceiptKey: cfg.ReceiptKey, CursorKey: cfg.CursorKey})
	if err != nil {
		return nil, err
	}
	if _, err = a.service.Configuration(ctx, ""); err != nil {
		return nil, err
	}
	if cfg.Enabled {
		resources, e := config.LoadWhisperResources(ctx, cfg.CatalogFile)
		if e != nil || !moderation.CareResourcesReady(resources.Care) {
			return nil, command.ErrUnavailable
		}
	}

	for _, spec := range []struct {
		db  *gorm.DB
		out **moderation.Service
	}{{review, &a.reviews}, {crisis, &a.crises}, {api, &a.student}} {
		*spec.out, err = moderation.New(spec.db, log, r.codec, moderation.Config{Command: cfg.Command, CursorKey: cfg.GovernanceKey, MaxEntries: 100})
		if err != nil {
			return nil, err
		}
	}
	a.deletion, err = privacy.New(priv, r.journal, r.codec, r.gateway, log, r.privacyConfig())
	if err != nil {
		return nil, err
	}
	pcfg := r.privacyConfig()
	pcfg.Key = cfg.EvidenceKey
	a.evidence, err = privacy.New(evidence, r.journal, r.codec, r.gateway, log, pcfg)
	if err != nil {
		return nil, err
	}
	for _, spec := range []struct {
		db  *gorm.DB
		out **safety.Service
	}{{api, &a.studentSafety}, {review, &a.adminSafety}} {
		*spec.out, err = safety.New(spec.db, log, r.codec, safety.Config{Command: cfg.Command, Key: cfg.SafetyKey, MaxTextBytes: 65536})
		if err != nil {
			return nil, err
		}
	}
	return a, nil
}

// Register 一次注册全部树洞操作；旧安全及注销路由已由宿主装配时复用其边界。
// 失败时宿主必须丢弃该router，禁止监听局部注册结果。
func (a *API) Register(router *gin.Engine) (err error) {
	defer finish(a.r.log, "api_register", time.Now(), &err)
	if router == nil {
		return command.ErrInvalid
	}
	defer func() {
		if recover() != nil {
			err = command.ErrInvalid
		}
	}()
	exists := func(method, path string) bool {
		for _, route := range router.Routes() {
			if route.Method == method && route.Path == path {
				return true
			}
		}
		return false
	}
	if exists("GET", "/api/v1/whispers/config") {
		return command.ErrInvalid
	}
	for _, register := range []func() error{
		func() error { return httpapi.RegisterWhispers(router, a.service) }, func() error { return httpapi.RegisterWhisperInteractions(router, a.service) },
		func() error { return httpapi.RegisterWhisperCapsules(router, a.service) }, func() error { return httpapi.RegisterInteractionBlocks(router, a.service) },
		func() error {
			return httpapi.RegisterWhisperGovernance(router, a.reviews, a.crises, a.student, a.evidence)
		},
	} {
		if err = register(); err != nil {
			return
		}
	}
	if !exists("GET", "/api/v1/me/blocks") {
		if err = httpapi.RegisterSafety(router, a.studentSafety, a.adminSafety); err != nil {
			return
		}
	}
	if !exists("POST", "/api/v1/me/deletion-requests") {
		err = httpapi.RegisterPrivacy(router, a.deletion)
	}
	return
}

// Ready 复核本模块依赖及资料；旧宿主仍负责其余认证、对象和业务就绪条件。
func (a *API) Ready(ctx context.Context) error {
	for _, db := range a.databases {
		pool, e := db.DB()
		if e != nil || pool.PingContext(ctx) != nil {
			return command.ErrUnavailable
		}
	}
	if a.client.Ping(ctx).Err() != nil {
		return command.ErrUnavailable
	}
	if e := a.r.check(ctx, a.databases[0]); e != nil {
		return e
	}
	if a.r.cfg.Enabled {
		if e := a.r.rules(ctx, a.databases[1]); e != nil {
			return e
		}
	}
	if e := a.r.gateway.ready(ctx); e != nil {
		return e
	}
	_, e := a.service.Configuration(ctx, "")
	return e
}

// Close 不关闭宿主拥有的Redis，必须在HTTP请求结束后调用。
func (a *API) Close() error { return a.r.close() }
