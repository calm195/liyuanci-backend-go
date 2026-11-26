package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/identity"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

func wechatPublicText(codec *securedata.Codec, client *gateway.WeChat) command.PublicTextCheck {
	return func(ctx context.Context, db *gorm.DB, actor string, scene int, fields []string) error {
		text := strings.TrimSpace(strings.Join(fields, "\n"))
		if text == "" {
			return nil
		}
		// 所有分段共享一份调用预算，避免慢供应商长时间占用业务事务。
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		openID, err := identity.ReadOpenID(ctx, db, codec, actor)
		if err != nil {
			return command.ErrUnavailable
		}
		defer clear(openID)
		return checkWeChatTextChunks(ctx, client, string(openID), scene, []rune(text))
	}
}

func checkWeChatTextChunks(ctx context.Context, client *gateway.WeChat, openID string, scene int, text []rune) error {
	const size, overlap = 2500, 128
	var review error
	for start := 0; start < len(text); start += size - overlap {
		end := min(start+size, len(text))
		err := client.CheckPublicText(ctx, openID, scene, string(text[start:end]))
		if errors.Is(err, command.ErrContentReview) {
			review = err
		} else if err != nil {
			return err
		}
		if end == len(text) {
			break
		}
	}
	return review
}

// configurePublicText 为开发环境独立注册的悄悄话路由使用同一小程序凭据。
func (a *WhisperAPI) configurePublicText(appID, secret string) error {
	if appID == "" && secret == "" {
		return nil
	}
	r := a.runtime.r
	httpClient := &http.Client{Timeout: r.cfg.ProviderTimeout}
	wechat, err := gateway.NewWeChat(httpClient, appID, secret, r.log)
	if err != nil {
		return err
	}
	r.closes = append(r.closes, func() error {
		httpClient.CloseIdleConnections()
		return nil
	})
	a.services.service.SetPublicTextCheck(wechatPublicText(r.codec, wechat))
	return nil
}
