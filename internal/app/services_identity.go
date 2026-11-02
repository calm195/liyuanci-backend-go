package app

import (
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/modules/verification"
)

// buildIdentity 构造身份会话、学生卡和资格认证，动作提示取实际已装配模块。
func (s *serviceSet) buildIdentity(d ServiceDependencies, cfg ServiceConfig) error {
	var e error
	cfg.Auth.DeletionRequests = s.accountDeletion != nil
	cfg.Auth.Safety = s.studentSafety != nil
	cfg.Auth.Wishes = s.wishes != nil
	// 路由由本构造器整体注册，动作提示只在同一成功装配中开启，不采纳调用方的过期标志。
	cfg.Auth.StudentCardAssets = true
	cfg.Auth.VerificationRequests = true
	s.auth, e = studentauth.New(d.StudentDB, d.Logger, d.Exchange, d.Identity, d.Limiter, cfg.Auth)
	if e != nil {
		return e
	}
	s.cards, e = studentcard.New(d.StudentDB, d.Logger, s.auth, d.Codec, d.Objects, d.Scanner, cfg.Card)
	if e != nil {
		return e
	}
	s.students, e = verification.New(d.StudentDB, d.Logger, d.Codec, d.Identity, d.Rules, d.Objects, cfg.Verification)
	if e != nil {
		return e
	}
	s.reviews, e = verification.New(d.ReviewDB, d.Logger, d.Codec, d.Identity, d.Rules, d.Objects, cfg.Verification)
	if e != nil {
		return e
	}
	return nil
}
