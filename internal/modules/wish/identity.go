package wish

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type identityCapabilityKey struct{}

// WithIdentityCapability 由HTTP边界注明客户端是否支持无账号关联的作者结构。
func WithIdentityCapability(ctx context.Context, supported bool) context.Context {
	return context.WithValue(ctx, identityCapabilityKey{}, supported)
}

func identitySupported(ctx context.Context) bool {
	supported, exists := ctx.Value(identityCapabilityKey{}).(bool)
	return !exists || supported
}

func (s *Service) displayID(purpose, scope, id string) string {
	mac := hmac.New(sha256.New, s.receiptKey)
	mac.Write([]byte(purpose + "\x00" + scope + "\x00" + id))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

func (s *Service) publicAuthor(ctx context.Context, a aggregate, tags IdentityTags) PublicAuthor {
	if a.owner.HideIdentityTags {
		tags = IdentityTags{}
	}
	if a.row.IdentityMode == "semi" {
		alias := "心愿友" + s.displayID("wish.alias", a.row.ID, a.row.OwnerID)
		return PublicAuthor{ID: "", Nickname: alias, DisplayID: alias, IdentityTags: tags, ProfileAvailable: false}
	}
	avatar := a.account.AvatarURL
	if a.account.AvatarID != nil {
		avatar = ""
		if s.avatarReader != nil {
			avatar, _ = s.avatarReader.AvatarURL(ctx, "avatars/"+*a.account.AvatarID+".png")
		}
	}
	return PublicAuthor{ID: a.row.OwnerID, Nickname: a.account.Nickname, DisplayID: "LY" + s.displayID("account.display", "", a.row.OwnerID), AvatarURL: avatar, IdentityTags: tags, ProfileAvailable: true}
}

// ReadAuthorProjection 为评论领域输出受该心愿上下文约束的作者，半匿名场景不暴露账号关联。
func (s *Service) ReadAuthorProjection(ctx context.Context, db *gorm.DB, wishID, actorID string) (PublicAuthor, error) {
	a, e := readAggregate(ctx, db, wishID, false)
	if e != nil {
		return PublicAuthor{}, e
	}
	if actorID == a.row.OwnerID {
		return s.publicAuthor(ctx, a, a.snapshot.Review.Tags), nil
	}
	var actor entity.Account
	if e = db.WithContext(ctx).Where("id=? AND lifecycle='active'", actorID).Take(&actor).Error; e != nil {
		return PublicAuthor{}, command.ErrNotFound
	}
	if a.row.IdentityMode == "semi" {
		alias := "心愿友" + s.displayID("wish.alias", wishID, actorID)
		return PublicAuthor{Nickname: alias, DisplayID: alias}, nil
	}
	a.row.OwnerID = actorID
	a.account = actor
	a.owner.HideIdentityTags = true
	return s.publicAuthor(ctx, a, IdentityTags{}), nil
}

// enrichWishMetadata 以一组目标批量读取有效申请计数，不为每张卡片新增统计请求。
func (s *Service) enrichWishMetadata(ctx context.Context, db *gorm.DB, items []PublicContent, actor string, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	var counts []struct {
		WishID string
		Count  int64
	}
	e := db.WithContext(ctx).Table("public.applications a").Select("a.wish_id,count(*) AS count").
		Joins("JOIN public.wishes w ON w.id=a.wish_id AND w.current_content_version=a.wish_content_version").
		Joins("JOIN public.accounts actor ON actor.id=a.applicant_id AND actor.lifecycle='active' AND actor.verification='verified'").
		Joins("JOIN public.accounts owner ON owner.id=w.owner_id AND owner.auth_epoch=a.owner_epoch").
		Joins("JOIN public.moderation_tasks t ON t.id=a.review_task_id AND t.status='approved' AND t.decision='approved' AND t.subject_epoch=actor.auth_epoch AND t.content_version=a.content_version AND t.restriction_epoch=w.restriction_epoch").
		Joins("JOIN public.content_registry r ON r.id=t.content_id AND r.kind='application' AND r.entity_id=a.id AND r.content_version=a.content_version AND r.status='approved'").
		Where("a.wish_id IN ? AND a.status IN ('active','accepted') AND a.applicant_id<>w.owner_id", ids).
		Where("t.hold_epoch=COALESCE((SELECT max(h.hold_epoch) FROM public.privacy_holds h WHERE h.target_kind='wish' AND h.target_id=w.id),0)").
		Where("NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=r.id AND x.active)").
		Where("NOT EXISTS(SELECT 1 FROM public.blocks b WHERE (b.owner_id=w.owner_id AND b.target_id=a.applicant_id) OR (b.owner_id=a.applicant_id AND b.target_id=w.owner_id))").
		Where("NOT EXISTS(SELECT 1 FROM public.penalties p WHERE p.account_id=actor.id AND p.revoked_at IS NULL AND p.action_kind IN ('mute','ban') AND p.starts_at<=? AND (p.ends_at IS NULL OR p.ends_at>?))", now, now).
		Group("a.wish_id").Scan(&counts).Error
	if e != nil {
		return e
	}
	byID := map[string]int64{}
	for _, count := range counts {
		byID[count.WishID] = count.Count
	}
	for i := range items {
		items[i].ApplicationCount = byID[items[i].ID]
		items[i].IsOwner = actor != "" && items[i].OwnerID == actor
	}
	return s.EnrichInteractionStats(ctx, db, items, actor, now)
}

func (s *Service) enrichResults(ctx context.Context, db *gorm.DB, results []*Result, actor string, now time.Time) error {
	items := make([]PublicContent, 0, len(results))
	visible := make([]*Result, 0, len(results))
	for _, result := range results {
		if result.PrivacyReviewRequired {
			continue
		}
		value := PublicContent{ID: result.ID, IsOwner: result.IsOwner}
		if result.Public != nil {
			value = *result.Public
		} else if result.Participant != nil {
			value = *result.Participant
		} else if result.IsOwner {
			value.OwnerID = actor
		}
		items = append(items, value)
		visible = append(visible, result)
	}
	if err := s.enrichWishMetadata(ctx, db, items, actor, now); err != nil {
		return err
	}
	for i, result := range visible {
		v := items[i]
		result.ApplicationCount = v.ApplicationCount
		result.CommentCount = v.CommentCount
		result.SupplementCount = v.SupplementCount
		result.ViewCount = v.ViewCount
		result.Favorited = v.Favorited
		result.CommentsOpen = v.CommentsOpen
		result.CommentsEpoch = v.CommentsEpoch
		if result.Public != nil {
			*result.Public = v
		}
		if result.Participant != nil {
			*result.Participant = v
		}
	}
	return nil
}
