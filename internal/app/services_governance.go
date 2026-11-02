package app

import (
	"bytes"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/modules/safety"
)

// buildResources 构造学生和后台资料服务，各入口保留独立领域授权。
func (s *serviceSet) buildResources(d ServiceDependencies, cfg ServiceConfig) error {
	var e error
	if cfg.Resources != nil {
		s.studentResources, e = resourceplaza.New(d.WishDB, d.Logger, d.Codec, *cfg.Resources)
		if e != nil {
			return e
		}
		s.adminResources, e = resourceplaza.New(d.ReviewDB, d.Logger, d.Codec, *cfg.Resources)
		if e != nil {
			return e
		}
	}
	return nil
}

// buildSafety 构造学生和后台安全服务，处罚效果仍由领域事务执行。
func (s *serviceSet) buildSafety(d ServiceDependencies, cfg ServiceConfig) error {
	var e error
	if cfg.Safety != nil {
		if s.wishes == nil {
			return command.ErrInvalid
		}
		keys := [][]byte{cfg.Auth.SessionKey, cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey, cfg.Wish.ReceiptKey}
		if cfg.Chat != nil {
			keys = append(keys, cfg.Chat.Key)
		}
		if cfg.Moderation != nil {
			keys = append(keys, cfg.Moderation.CursorKey)
		}
		for _, key := range keys {
			if bytes.Equal(key, cfg.Safety.Key) {
				return command.ErrInvalid
			}
		}
		s.studentSafety, e = safety.New(d.WishDB, d.Logger, d.Codec, *cfg.Safety)
		if e != nil {
			return e
		}
		s.adminSafety, e = safety.New(d.ReviewDB, d.Logger, d.Codec, *cfg.Safety)
		if e != nil {
			return e
		}
	}
	return nil
}

// buildPrivacy 构造账号与证据删除服务，独立意图账本和用途密钥为必需边界。
func (s *serviceSet) buildPrivacy(d ServiceDependencies, cfg ServiceConfig) error {
	var e error
	for _, spec := range []struct {
		cfg *privacy.Config
		db  *gorm.DB
		out **privacy.Service
	}{
		{cfg.Privacy, d.PrivacyDB, &s.accountDeletion}, {cfg.EvidenceDeletion, d.EvidenceDeletionDB, &s.evidenceDeletion},
	} {
		if spec.cfg == nil {
			continue
		}
		if spec.db == nil || d.DeletionJournal == nil {
			return command.ErrInvalid
		}
		if _, e := spec.db.DB(); e != nil {
			return command.ErrInvalid
		}
		keys := [][]byte{cfg.Auth.SessionKey, cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey}
		if cfg.Wish != nil {
			keys = append(keys, cfg.Wish.ReceiptKey)
		}
		if cfg.Chat != nil {
			keys = append(keys, cfg.Chat.Key)
		}
		if cfg.Safety != nil {
			keys = append(keys, cfg.Safety.Key)
		}
		if cfg.Moderation != nil {
			keys = append(keys, cfg.Moderation.CursorKey)
		}
		if cfg.Crisis != nil {
			keys = append(keys, cfg.Crisis.CursorKey)
		}
		for _, key := range keys {
			if bytes.Equal(key, spec.cfg.Key) {
				return command.ErrInvalid
			}
		}
		*spec.out, e = privacy.New(spec.db, d.DeletionJournal, d.Codec, d.Eraser, d.Logger, *spec.cfg)
		if e != nil {
			return e
		}
	}
	return nil
}

// buildModeration 构造内容与危机审核，复核数据库和跨用途密钥隔离。
func (s *serviceSet) buildModeration(d ServiceDependencies, cfg ServiceConfig) error {
	var e error
	if cfg.Moderation != nil {
		for _, key := range [][]byte{cfg.Auth.SessionKey, cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey} {
			if bytes.Equal(key, cfg.Moderation.CursorKey) {
				return command.ErrInvalid
			}
		}
		if cfg.Wish != nil && bytes.Equal(cfg.Wish.ReceiptKey, cfg.Moderation.CursorKey) {
			return command.ErrInvalid
		}
		s.contentReviews, e = moderation.New(d.ReviewDB, d.Logger, d.Codec, *cfg.Moderation)
		if e != nil {
			return e
		}
	}
	if cfg.Crisis != nil {
		if d.CrisisDB == nil {
			return command.ErrInvalid
		}
		if _, e := d.CrisisDB.DB(); e != nil {
			return command.ErrInvalid
		}
		keys := [][]byte{cfg.Auth.SessionKey, cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey}
		if cfg.Wish != nil {
			keys = append(keys, cfg.Wish.ReceiptKey)
		}
		if cfg.Chat != nil {
			keys = append(keys, cfg.Chat.Key)
		}
		if cfg.Safety != nil {
			keys = append(keys, cfg.Safety.Key)
		}
		if cfg.Moderation != nil {
			keys = append(keys, cfg.Moderation.CursorKey)
		}
		for _, k := range keys {
			if bytes.Equal(k, cfg.Crisis.CursorKey) {
				return command.ErrInvalid
			}
		}

		s.crisisReviews, e = moderation.New(d.CrisisDB, d.Logger, d.Codec, *cfg.Crisis)
		if e != nil {
			return e
		}
	}
	return nil
}
