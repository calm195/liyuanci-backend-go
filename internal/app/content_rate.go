package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/redis/authrate"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
)

type contentCounter struct {
	limiter *authrate.Limiter
	key     []byte
}

func (c contentCounter) Allow(ctx context.Context, subject string) (bool, error) {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte("content-rate\x00" + subject))
	return c.limiter.Allow(ctx, hex.EncodeToString(mac.Sum(nil)))
}

func contentRate(client redis.UniversalClient, db *gorm.DB, key []byte, cfg config.Content) (gin.HandlerFunc, error) {
	read, err := authrate.NewScoped(client, "content-read:v1:", cfg.Window, cfg.ReadLimit)
	if err != nil {
		return nil, err
	}
	export, err := authrate.NewScoped(client, "content-export:v1:", cfg.Window, cfg.ExportLimit)
	if err != nil {
		return nil, err
	}
	return httpapi.ContentRate(
		contentCounter{limiter: read, key: key}, contentCounter{limiter: export, key: key},
		func(ctx context.Context, token string, admin bool) (string, error) {
			return rateAccount(ctx, db, token, admin)
		}, cfg.Window,
	), nil
}

// rateAccount 仅分配限流额度。领域服务继续在事务内复核会话、账号和岗位授权。
func rateAccount(ctx context.Context, db *gorm.DB, token string, admin bool) (string, error) {
	tokenHash := sha256.Sum256([]byte(token))
	query := db.WithContext(ctx).Table("public.sessions AS s").Select("a.id").
		Where("s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()", tokenHash[:]).
		Where("s.auth_epoch = a.auth_epoch")
	if admin {
		query = query.Joins("JOIN public.admin_accounts AS a ON a.id = s.admin_id").
			Where("s.subject_kind = 'admin' AND a.status = 'active'")
	} else {
		query = query.Joins("JOIN public.accounts AS a ON a.id = s.account_id").
			Where("s.subject_kind = 'student' AND a.lifecycle = 'active'")
	}
	var id string
	err := query.Scan(&id).Error
	return id, err
}

func contentSettings(options []config.Content) config.Content {
	if len(options) != 0 {
		return options[0]
	}
	return config.DefaultContent()
}
