package app

import (
	"context"

	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
)

// buildMatchmaking 在正式审核模块启用时装配活动，复用当前展示年级及词库。
func (s *serviceSet) buildMatchmaking(d ServiceDependencies, cfg ServiceConfig) error {
	if cfg.Moderation == nil {
		return nil
	}
	checker := &moderation.ActivityTextChecker{}
	grades := func(ctx context.Context) []string {
		out := []string{}
		if cfg.Auth.ProfileGrades != nil {
			for _, grade := range cfg.Auth.ProfileGrades(ctx) {
				out = append(out, grade.Code)
			}
		}
		return out
	}
	var err error
	s.matchmaking, err = matchmaking.New(d.StudentDB, d.Logger, d.Codec, matchmaking.Config{
		Command:   cfg.Auth.Command,
		Grades:    grades,
		CheckText: checker.Check,
	})
	if err != nil {
		return err
	}
	if s.chats != nil {
		s.chats.SetMatchmakingNotificationTarget(matchmaking.NotificationTarget)
	}
	return nil
}
