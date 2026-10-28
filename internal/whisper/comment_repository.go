package whisper

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

type commentAggregate struct {
	comment                       entity.WhisperComment
	version                       entity.WhisperCommentVersion
	author                        entity.Account
	privacy, isolated, restricted bool
}

func loadComment(ctx context.Context, db *gorm.DB, id string) (a commentAggregate, err error) {
	db = db.WithContext(ctx)
	if err = db.Where("id=?", id).Take(&a.comment).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return a, command.ErrNotFound
	} else if err != nil {
		return
	}
	if err = db.Where("comment_id=? AND content_version=?", id, a.comment.ContentVersion).Take(&a.version).Error; err != nil {
		return
	}
	if err = db.Where("id=?", a.comment.AuthorID).Take(&a.author).Error; err != nil {
		return
	}
	var flags struct{ Privacy, Isolated, Restricted bool }
	err = db.Raw(`SELECT EXISTS(SELECT 1 FROM public.privacy_holds WHERE target_kind='whisper_comment' AND target_id=? AND status='active') AS privacy,EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE c.kind='whisper_comment' AND c.entity_id=? AND x.active AND x.kind='crisis') AS isolated,EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id WHERE c.kind='whisper_comment' AND c.entity_id=? AND x.active) AS restricted`, id, id, id).Scan(&flags).Error
	a.privacy, a.isolated, a.restricted = flags.Privacy, flags.Isolated, flags.Restricted
	if err == nil && a.version.NicknameVersion != nil {
		var n entity.WhisperNicknameVersion
		n, err = nicknameRow(ctx, db, a.comment.AuthorID, *a.version.NicknameVersion)
		if err == nil {
			var safe bool
			safe, err = nicknameSafe(ctx, db, n)
			a.restricted = a.restricted || !safe
		}
	}
	return
}

func interactionPlan(ctx context.Context, db *gorm.DB, postID, commentID string) (p command.Discovery, err error) {
	db = db.WithContext(ctx)
	if commentID != "" {
		var c entity.WhisperComment
		if err = db.Select("id,author_id,post_id").Where("id=?", commentID).Take(&c).Error; err != nil {
			return p, missingRow(err)
		}
		p.Accounts = append(p.Accounts, c.AuthorID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperComment, ID: commentID})
		if c.PostID != nil {
			postID = *c.PostID
		}
	}
	if postID != "" {
		var p0 entity.WhisperPost
		if err = db.Select("id,owner_id").Where("id=?", postID).Take(&p0).Error; err != nil {
			return p, missingRow(err)
		}
		p.Accounts = append(p.Accounts, p0.OwnerID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WhisperPost, ID: postID})
	}
	return
}

func missingRow(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return command.ErrNotFound
	}
	return err
}

func interactionBlocked(ctx context.Context, db *gorm.DB, actor, owner string) error {
	var n int64
	if e := db.WithContext(ctx).Model(&entity.Block{}).Where("(owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?)", actor, owner, owner, actor).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return command.ErrBlockedInteraction
	}
	return nil
}

func (s *Service) commentContext(ctx context.Context, db *gorm.DB, now time.Time, id, actor string, epoch int64) (aggregate, error) {
	c, e := s.resources(ctx)
	if e != nil {
		return aggregate{}, e
	}
	if _, e = s.publicPostBase(ctx, db, now, id, audience{ID: actor}, c); e != nil {
		return aggregate{}, e
	}
	p, e := loadPost(ctx, db, id)
	if e != nil {
		return p, e
	}
	if !p.post.CommentsOpen || p.post.CommentsEpoch != epoch {
		return p, command.ErrBlockedInteraction
	}
	return p, interactionBlocked(ctx, db, actor, p.post.OwnerID)
}

// visibleCommentsQuery 与列表和权威计数共用过滤，不因关闭评论隐藏既有公开项。
func visibleCommentsQuery(ctx context.Context, db *gorm.DB, postID string, parentVersion int64, now time.Time) *gorm.DB {
	return db.WithContext(ctx).Table("public.whisper_comments AS c").Joins("JOIN public.whisper_comment_versions v ON v.comment_id=c.id AND v.content_version=c.content_version").Joins("JOIN public.accounts a ON a.id=c.author_id").Where(`c.post_id=? AND c.parent_content_version=? AND c.lifecycle='active' AND c.review_state='approved' AND c.published_at IS NOT NULL AND v.review_state='approved' AND v.public_body IS NOT NULL AND v.public_identity IS NOT NULL AND v.school_related IS NOT NULL AND a.lifecycle='active'
 AND NOT EXISTS(SELECT 1 FROM public.penalties p WHERE p.account_id=a.id AND p.revoked_at IS NULL AND p.action_kind IN ('mute','ban') AND p.starts_at<=? AND (p.ends_at IS NULL OR p.ends_at>?))
 AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry r ON r.id=x.content_id WHERE r.kind='whisper_comment' AND r.entity_id=c.id AND x.active)
 AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind='whisper_comment' AND h.target_id=c.id AND h.status='active')
 AND (v.nickname_version IS NULL OR EXISTS(SELECT 1 FROM public.whisper_nickname_versions n WHERE n.account_id=c.author_id AND n.nickname_version=v.nickname_version AND n.review_state='approved' AND n.revoked_at IS NULL AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=n.content_registry_id AND x.active) AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind='whisper_nickname' AND h.target_id=n.content_registry_id AND h.status='active')))`, postID, parentVersion, now, now)
}
