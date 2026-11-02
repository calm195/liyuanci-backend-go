package app

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/identity"
)

// InitializeBusiness 仅供显式初始化命令调用，不迁移、不替换历史身份密钥、不重置已有后台口令。
// 可选词库通过现有授权服务发布完整快照并保留审计；已存在的生效快照只能等值重放。
func InitializeBusiness(ctx context.Context, c config.Business, environment, login string, wordlists map[string][]string, client redis.UniversalClient, log *zap.Logger) (err error) {
	if log == nil || client == nil || login == "" || config.ValidateBusiness(c, environment, "bootstrap") != nil {
		return command.ErrInvalid
	}
	defer finish(log, "business_initialize", time.Now(), &err)
	if wordlists != nil {
		if len(wordlists) != 3 || c.Runtime.DatabaseDSN == "" {
			return command.ErrInvalid
		}
		for _, kind := range []string{"crisis", "school", "ordinary_violation"} {
			if len(wordlists[kind]) == 0 {
				return command.ErrInvalid
			}
		}
	}
	r, e := openResources(ctx, c.Runtime, log)
	if e != nil {
		return e
	}
	defer func() {
		if r.close() != nil {
			err = command.ErrUnavailable
		}
	}()
	g, ok := r.gateway.(*gateway.Client)
	if !ok {
		return command.ErrInvalid
	}
	b := gateway.NewBusiness(g, c.LookupKeyRefs, 0)
	if e = b.ReadyFor(ctx, "bootstrap"); e != nil {
		return e
	}
	db, e := r.pool(ctx)
	if e != nil {
		return e
	}
	store, e := identity.New(db, r.codec, b, log)
	if e != nil {
		return e
	}
	for _, purpose := range []string{"openid", "student_number"} {
		if e = store.Configure(ctx, purpose, c.LookupKeyRefs[0]); e != nil {
			return e
		}
	}
	adminDB, e := r.pool(ctx)
	if e != nil {
		return e
	}
	limiter, e := adminauth.NewRedisLimiter(client, c.LoginWindow, c.LoginLimit)
	if e != nil {
		return e
	}
	password, e := config.ValidateSecret(c.AdminPassword, 12)
	if e != nil {
		return e
	}
	admin, e := adminauth.New(adminDB, limiter, log, adminauth.Config{SessionTTL: c.AdminSessionTTL, Command: command.Config(c.Runtime.Command), InitialPassword: password, ReceiptKey: c.AdminKey})
	if e != nil {
		return e
	}
	var count int64
	if adminDB.WithContext(ctx).Table("admin_accounts").Count(&count).Error != nil {
		return command.ErrUnavailable
	}
	if count == 0 {
		if _, e = admin.Bootstrap(ctx, login); e != nil {
			return e
		}
	}
	if wordlists == nil {
		return nil
	}
	session, e := admin.Login(ctx, login, password, "business-initializer", uuid.NewString())
	if e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), c.Runtime.Command.Timeout)
		defer cancel()
		if admin.Logout(cleanup, session.Token, uuid.NewString(), uuid.NewString()) != nil {
			err = command.ErrUnavailable
		}
	}()
	review, e := r.pool(ctx)
	if e != nil {
		return e
	}
	service, e := moderation.New(review, log, r.codec, moderation.Config{Command: command.Config(c.Runtime.Command), CursorKey: c.Runtime.GovernanceKey, MaxEntries: 2000})
	if e != nil {
		return e
	}
	for _, kind := range []string{"crisis", "school", "ordinary_violation"} {
		page, e := service.Wordlists(ctx, session.Token, uuid.NewString(), "", 100)
		if e != nil {
			return e
		}
		for _, head := range page.Heads {
			if head.Kind != kind {
				continue
			}
			if head.EffectiveID != nil {
				current, e := service.Wordlist(ctx, session.Token, uuid.NewString(), *head.EffectiveID)
				if e != nil {
					return e
				}
				want := slices.Clone(wordlists[kind])
				slices.Sort(want)
				slices.Sort(current.Entries)
				if !slices.Equal(want, current.Entries) {
					return command.ErrConflict
				}
				continue
			}
			draft, e := service.CreateWordlist(ctx, session.Token, uuid.NewString(), uuid.NewString(), moderation.WordlistInput{Kind: kind, Entries: wordlists[kind], ExpectedVersion: head.Version})
			if e != nil {
				return e
			}
			_, e = service.PublishWordlist(ctx, session.Token, uuid.NewString(), uuid.NewString(), draft.ID, moderation.WordlistPublish{ExpectedVersion: draft.Version, ExpectedHeadVersion: head.Version + 1})
			if e != nil {
				return e
			}
		}
	}
	return r.rules(ctx, review)
}
