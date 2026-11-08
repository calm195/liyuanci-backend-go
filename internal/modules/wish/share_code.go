package wish

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ShareCodes 为当前公开引用生成可扫描的微信小程序码 PNG。
type ShareCodes interface {
	MiniappCode(ctx context.Context, page, scene, env string) ([]byte, error)
}

// ShareCode 将 PNG 以 JSON 的 Base64 字节字段返回给小程序。
type ShareCode struct {
	Image []byte `json:"image_base64"`
}

// SetShareCodes 在服务启动前注入微信小程序码供应商。
func (s *Service) SetShareCodes(codes ShareCodes) {
	s.shareCodes = codes
}

func sharePage(kind string) string {
	if kind == "story" {
		return "pages/stories/index"
	}
	return "pages/wish-detail/index"
}

// ShareCode 仅允许普通会话为当前仍可访问的分享引用请求小程序码。
func (s *Service) ShareCode(ctx context.Context, token, rid, ref, env string) (out ShareCode, err error) {
	defer s.storyReadDone("share.code", rid, time.Now(), &err)
	if !validWishID(ref) || (env != "develop" && env != "trial" && env != "release") {
		return out, command.ErrInvalid
	}
	var kind string
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor, err := currentReader(ctx, db, token, now)
		if err != nil {
			return err
		}
		var count int64
		if err := db.Model(&entity.Penalty{}).Where(
			"account_id=? AND action_kind IN ? AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)",
			actor, []string{"mute", "ban"}, now, now,
		).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return command.ErrForbidden
		}
		kind, _, err = s.publicShare(ctx, db, rid, ref, now)
		return err
	})
	if err != nil {
		return out, err
	}
	if s.shareCodes == nil {
		return out, command.ErrUnavailable
	}
	// 公开资格复核完成后才请求微信，避免外部 I/O 占用数据库事务。
	png, err := s.shareCodes.MiniappCode(ctx, sharePage(kind), strings.ReplaceAll(ref, "-", ""), env)
	if err != nil || len(png) == 0 {
		return out, command.ErrUnavailable
	}
	return ShareCode{Image: png}, nil
}
