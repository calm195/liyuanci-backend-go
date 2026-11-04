package app

import (
	"bytes"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// buildInteraction 构造心愿与聊天，复核父模块启用与独立用途密钥。
func (s *serviceSet) buildInteraction(d ServiceDependencies, cfg ServiceConfig) error {
	var e error
	if cfg.Wish != nil {
		if d.WishDB == nil {
			return command.ErrInvalid
		}
		_, failure := d.WishDB.DB()
		if failure != nil {
			return command.ErrInvalid
		}
		for _, key := range [][]byte{cfg.Auth.SessionKey, cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey} {
			if bytes.Equal(key, cfg.Wish.ReceiptKey) {
				return command.ErrInvalid
			}
		}
		var failureNew error
		s.wishes, failureNew = wish.NewService(d.WishDB, d.Logger, d.Codec, d.Grades, *cfg.Wish)
		if failureNew != nil {
			return failureNew
		}
	}
	if cfg.Chat != nil {
		if s.wishes == nil {
			return command.ErrInvalid
		}
		for _, key := range [][]byte{cfg.Auth.SessionKey, cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey, cfg.Wish.ReceiptKey} {
			if bytes.Equal(key, cfg.Chat.Key) {
				return command.ErrInvalid
			}
		}
		if cfg.Moderation != nil && bytes.Equal(cfg.Moderation.CursorKey, cfg.Chat.Key) {
			return command.ErrInvalid
		}
		s.chats, e = chat.New(d.WishDB, d.Logger, d.Codec, *cfg.Chat)
		if e != nil {
			return e
		}
	}
	return nil
}
