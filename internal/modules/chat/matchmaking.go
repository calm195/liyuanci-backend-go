package chat

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func (s *Snapshot) loadMatchmaking(db *gorm.DB) error {
	if err := db.Where("id=?", *s.Conversation.MatchmakingPairID).Take(&s.Pair).Error; err != nil {
		return err
	}
	if err := db.Where("id=?", s.Pair.ActivityID).Take(&s.Activity).Error; err != nil {
		return err
	}
	var members []entity.MatchmakingMember
	if err := db.Where("pair_id=?", s.Pair.ID).Order("account_id").Find(&members).Error; err != nil {
		return err
	}
	if len(members) != 2 || members[0].AccountID == members[1].AccountID {
		return command.ErrNotFound
	}
	if err := db.Where("id=?", members[0].AccountID).Take(&s.Requester).Error; err != nil {
		return err
	}
	if err := db.Where("id=?", members[1].AccountID).Take(&s.Helper).Error; err != nil {
		return err
	}
	if !members[0].Active || !members[1].Active {
		return nil
	}
	var count int64
	err := db.Table("public.matchmaking_members AS m").
		Joins("JOIN public.matchmaking_enrollments e ON e.id=m.enrollment_id AND e.activity_id=m.activity_id AND e.account_id=m.account_id AND e.profile_version=m.profile_version").
		Where("m.pair_id=? AND e.status='approved'", s.Pair.ID).Count(&count).Error
	s.PairMembersActive = count == 2
	return err
}

// AuthorizeRead 在小程序读取及举报时复核当前包分配权限；旧心愿会话保留只读规则。
func (s Snapshot) AuthorizeRead(ctx context.Context, db *gorm.DB, actor string, now time.Time) error {
	if !s.CanRead(actor) {
		return command.ErrNotFound
	}
	if s.Conversation.MatchmakingPairID != nil {
		err := Writable(ctx, db, s, now)
		if errors.Is(err, command.ErrForbidden) || errors.Is(err, command.ErrBlockedInteraction) {
			return command.ErrNotFound
		}
		return err
	}
	return nil
}

func (s Snapshot) parentHoldEpoch(ctx context.Context, db *gorm.DB) (int64, error) {
	if s.Conversation.MatchmakingPairID != nil {
		// 包分配无心愿隐私隔离记录，审核改由配对权限和会话写入代次约束。
		return 0, nil
	}
	return holdEpoch(ctx, db, s.Wish.ID)
}

// 分页前应用会话权限，隐藏的配对不会占用一页的名额。
const matchmakingConversationListFilter = `
mp.status='published' AND ma.status='published'
AND EXISTS (
	SELECT 1 FROM public.matchmaking_members self WHERE self.pair_id=mp.id AND self.account_id=?
)
AND (
	SELECT count(*) FROM public.matchmaking_members mm
	JOIN public.accounts am ON am.id=mm.account_id
	JOIN public.matchmaking_enrollments en ON en.id=mm.enrollment_id AND en.activity_id=mm.activity_id
		AND en.account_id=mm.account_id AND en.profile_version=mm.profile_version
	WHERE mm.pair_id=mp.id AND mm.active AND am.lifecycle='active' AND am.verification='verified' AND en.status='approved'
) = 2
AND NOT EXISTS (
	SELECT 1 FROM public.penalties pen
	JOIN public.matchmaking_members mm ON mm.account_id=pen.account_id AND mm.pair_id=mp.id
	WHERE pen.action_kind IN ('mute','ban') AND pen.revoked_at IS NULL AND pen.starts_at<=?
		AND (pen.ends_at IS NULL OR pen.ends_at>?)
)
AND NOT EXISTS (
	SELECT 1 FROM public.blocks bl
	JOIN public.matchmaking_members one ON one.account_id=bl.owner_id AND one.pair_id=mp.id
	JOIN public.matchmaking_members two ON two.account_id=bl.target_id AND two.pair_id=mp.id
)`
