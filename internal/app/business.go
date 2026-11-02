package app

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/identity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/modules/safety"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/modules/verification"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// BusinessAPI 组合共享运行资源、树洞与完整业务服务；各岗位仍由领域授权隔离。
type BusinessAPI struct {
	runtime    *apiRuntime
	whispers   *whisperServices
	Router     *gin.Engine
	cfg        config.Business
	gateway    *gateway.Business
	identityDB *gorm.DB
}

// OpenBusiness 只装配真实适配器；全部配置、结构、恢复、资料及依赖检查通过后才返回可监听路由。
func OpenBusiness(ctx context.Context, cfg config.Business, environment string, client redis.UniversalClient, log *zap.Logger) (out *BusinessAPI, err error) {
	if log == nil || config.ValidateBusiness(cfg, environment, "api") != nil {
		return nil, command.ErrInvalid
	}
	defer finish(log, "business_open", time.Now(), &err)
	a, whispers, e := openAPIComponents(ctx, cfg.Runtime, client, log)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			_ = a.close()
		}
	}()
	g, ok := a.r.gateway.(*gateway.Client)
	if !ok {
		return nil, command.ErrInvalid
	}
	b := &BusinessAPI{runtime: a, whispers: whispers, cfg: cfg, gateway: gateway.NewBusiness(g, cfg.LookupKeyRefs, cfg.Card.MaxBytes*2+65536)}
	student, e := a.r.pool(ctx)
	if e != nil {
		return nil, e
	}
	adminDB, e := a.r.pool(ctx)
	if e != nil {
		return nil, e
	}
	b.identityDB = student
	if e = b.Ready(ctx); e != nil {
		return nil, e
	}
	store, e := identity.New(student, a.r.codec, b.gateway, log)
	if e != nil {
		return nil, e
	}
	limiter, e := adminauth.NewRedisLimiter(client, cfg.LoginWindow, cfg.LoginLimit)
	if e != nil {
		return nil, e
	}
	secret, e := config.ValidateSecret(cfg.WeChatSecret, 16)
	if e != nil {
		return nil, e
	}
	httpClient := &http.Client{Timeout: cfg.Runtime.ProviderTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	a.r.closes = append(a.r.closes, func() error { httpClient.CloseIdleConnections(); return nil })
	exchange, e := gateway.NewWeChat(httpClient, cfg.WeChatAppID, secret, log)
	if e != nil {
		return nil, e
	}
	cmd := command.Config(cfg.Runtime.Command)
	pconf := a.r.privacyConfig()
	econf := pconf
	econf.Key = cfg.Runtime.EvidenceKey
	router, e := newServiceRouter(ServiceDependencies{
		StudentDB: student, ReviewDB: a.database, WishDB: a.database, CrisisDB: a.database, PrivacyDB: a.database, EvidenceDeletionDB: a.database,
		DeletionJournal: a.r.journal, Eraser: b.gateway, Logger: log, Ready: b.Ready, Exchange: exchange, Identity: store, Limiter: limiter, Codec: a.r.codec, Objects: b.gateway, Scanner: b.gateway, Rules: b.gateway, Grades: businessGrades{cfg.Runtime},
	}, ServiceConfig{
		Auth: studentauth.Config{SessionTTL: cfg.SessionTTL, ExchangeTTL: cfg.ExchangeTTL, SessionKey: cfg.SessionKey, Command: cmd},
		Card: businessCardConfig(cfg), Verification: verification.Config{Command: cmd, ReceiptKey: cfg.VerificationKey, MaxCardBytes: cfg.Card.MaxBytes},
		Wish:       &wish.ServiceConfig{Command: cmd, ReceiptKey: cfg.WishKey, MaxStoryBytes: cfg.MaxTextBytes},
		Chat:       &chat.Config{Command: cmd, Key: chat.DeriveKey(cfg.WishKey), MaxBodyBytes: cfg.MaxTextBytes},
		Safety:     &safety.Config{Command: cmd, Key: cfg.Runtime.SafetyKey, MaxTextBytes: cfg.MaxTextBytes},
		Resources:  &resourceplaza.Config{Command: cmd, Key: resourceplaza.DeriveKey(cfg.Runtime.SafetyKey)},
		Moderation: &moderation.Config{Command: cmd, CursorKey: cfg.Runtime.GovernanceKey, MaxEntries: 2000},
		Crisis:     &moderation.Config{Command: cmd, CursorKey: cfg.CrisisKey, MaxEntries: 2000},
		Privacy:    &pconf, EvidenceDeletion: &econf,
	})
	if e != nil {
		return nil, e
	}
	password, e := config.ValidateSecret(cfg.AdminPassword, 12)
	if e != nil {
		return nil, e
	}
	admin, e := adminauth.New(adminDB, limiter, log, adminauth.Config{Command: cmd, ReceiptKey: cfg.AdminKey, SessionTTL: cfg.AdminSessionTTL, InitialPassword: password})
	if e != nil {
		return nil, e
	}
	if e = httpapi.RegisterAdmin(router, admin); e != nil {
		return nil, e
	}
	if e = whispers.register(router); e != nil {
		return nil, e
	}
	b.Router = router
	return b, nil
}

// Ready 检查结构、身份检索代次、正式资料和供应商能力，不以healthz替代业务就绪。
func (b *BusinessAPI) Ready(ctx context.Context) error {
	if e := b.runtime.ready(ctx); e != nil {
		return e
	}
	if e := b.whispers.ready(ctx); e != nil {
		return e
	}
	if e := b.runtime.r.rules(ctx, b.runtime.database); e != nil {
		return e
	}
	if e := businessSchema(ctx, b.runtime.database); e != nil {
		return e
	}
	if e := b.gateway.ReadyFor(ctx, "api"); e != nil {
		return e
	}
	if e := lookupReady(ctx, b.identityDB, b.gateway); e != nil {
		return e
	}
	catalog, e := businessResources(ctx, b.cfg.Runtime)
	if e != nil {
		return e
	}
	var reports []whisper.Item
	if b.runtime.database.WithContext(ctx).Raw("SELECT code,label FROM public.report_types WHERE published").Scan(&reports).Error != nil || len(reports) != len(catalog.Catalog.Reports) {
		return command.ErrUnavailable
	}
	for _, item := range catalog.Catalog.Reports {
		if !slices.Contains(reports, item) {
			return command.ErrUnavailable
		}
	}
	return nil
}

// Close 在请求排空后释放完整业务拥有的共享资源，不关闭借用的Redis。
func (b *BusinessAPI) Close() error { return b.runtime.close() }

func businessSchema(ctx context.Context, db *gorm.DB) error {
	var ready bool
	if db.WithContext(ctx).Raw(`SELECT to_regclass('public.student_login_exchanges') IS NOT NULL AND to_regclass('public.student_card_payloads') IS NOT NULL AND to_regclass('public.resource_audits') IS NOT NULL AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.penalties') AND attname='source_kind' AND NOT attisdropped)`).Scan(&ready).Error != nil || !ready {
		return command.ErrUnavailable
	}
	return nil
}

func lookupReady(ctx context.Context, db *gorm.DB, g *gateway.Business) error {
	var rows []entity.IdentityLookupKey
	if db.WithContext(ctx).Find(&rows).Error != nil || len(rows) != 2 {
		return command.ErrUnavailable
	}
	for _, row := range rows {
		if row.Purpose != "openid" && row.Purpose != "student_number" {
			return command.ErrUnavailable
		}
		refs := []string{row.CurrentRef}
		if row.PreviousRef != nil {
			refs = append(refs, *row.PreviousRef)
		}
		for _, ref := range refs {
			if !g.HasLookupReference(ref) {
				return command.ErrUnavailable
			}
			probe, e := g.Digest(ctx, ref, row.Purpose, []byte("runtime-readiness-probe"))
			clear(probe)
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func businessCardConfig(c config.Business) studentcard.Config {
	return studentcard.Config{Command: command.Config(c.Runtime.Command), ReceiptKey: c.CardKey, DecodeSlots: c.Card.DecodeSlots, MaxBytes: c.Card.MaxBytes, MaxPixels: c.Card.MaxPixels, Lease: c.Card.Lease, RetryDelay: c.Card.RetryDelay, MaxAttempts: c.Card.MaxAttempts}
}

func businessResources(ctx context.Context, cfg WhisperSettings) (ResourcesFile, error) {
	r, e := loadResources(ctx, cfg)
	if e != nil || !moderation.CareResourcesReady(r.Care) || r.Catalog.Grades.Version == nil || len(r.Catalog.Grades.Items) == 0 || len(r.Catalog.Reports) == 0 || r.Catalog.Help.Status != "ready" || r.Catalog.Help.Version == nil || r.Catalog.Help.VerifiedAt == nil || len(r.Catalog.Help.Items) == 0 {
		return ResourcesFile{}, command.ErrUnavailable
	}
	return r, nil
}

type businessGrades struct{ cfg WhisperSettings }

// Allows 按当前已核验资源校验年级，资料读取失败不能放行新输入。
func (g businessGrades) Allows(ctx context.Context, value string) (bool, error) {
	r, e := loadResources(ctx, g.cfg)
	if e != nil || len(r.Catalog.Grades.Items) == 0 {
		return false, command.ErrUnavailable
	}
	for _, item := range r.Catalog.Grades.Items {
		if item.Code == value {
			return true, nil
		}
	}
	return false, nil
}
