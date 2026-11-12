package wish

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

func loadWishComment(ctx context.Context, db *gorm.DB, id string) (c entity.WishComment, v entity.WishCommentVersion, err error) {
	if err = db.WithContext(ctx).Where("id=?", id).Take(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = command.ErrNotFound
		}
		return
	}
	err = db.WithContext(ctx).Where("comment_id=? AND content_version=?", id, c.ContentVersion).Take(&v).Error
	return
}

// DiscoverWishComment 锁定评论双方与根楼后再锁审核任务，避免回复跨对象变更。
func DiscoverWishComment(ctx context.Context, db *gorm.DB, wishID, commentID string, root, reply *string) (p command.Discovery, err error) {
	if commentID != "" {
		c, _, e := loadWishComment(ctx, db, commentID)
		if e != nil {
			return p, e
		}
		wishID = c.WishID
		p.Accounts = append(p.Accounts, c.AuthorID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WishComment, ID: commentID})
		if root == nil {
			root = c.RootCommentID
		}
		if reply == nil {
			reply = c.ReplyToCommentID
		}
	}
	var w entity.Wish
	if err = db.WithContext(ctx).Select("id,owner_id").Where("id=?", wishID).Take(&w).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = command.ErrNotFound
		}
		return
	}
	p.Accounts = append(p.Accounts, w.OwnerID)
	p.Resources = append(p.Resources, command.Resource{Kind: command.Wish, ID: w.ID})
	for _, id := range []*string{root, reply} {
		if id == nil {
			continue
		}
		var c entity.WishComment
		if err = db.WithContext(ctx).Where("id=?", *id).Take(&c).Error; err != nil {
			return p, command.ErrNotFound
		}
		p.Accounts = append(p.Accounts, c.AuthorID)
		p.Resources = append(p.Resources, command.Resource{Kind: command.WishComment, ID: c.ID})
	}
	return
}

// visibleWishComments 将正文、父心愿、账号、治理及隐私判断用于列表与计数。
func visibleWishComments(ctx context.Context, db *gorm.DB, now time.Time) *gorm.DB {
	return db.WithContext(ctx).Table("public.wish_comments AS c").
		Joins("JOIN public.wish_comment_versions cv ON cv.comment_id=c.id AND cv.content_version=c.content_version").
		Joins("JOIN public.wishes w ON w.id=c.wish_id AND w.current_content_version=c.parent_content_version").
		Joins("JOIN public.wish_versions v ON v.wish_id=w.id AND v.content_version=w.current_content_version").
		Joins("JOIN public.accounts ca ON ca.id=c.author_id AND ca.lifecycle='active'").
		Joins("JOIN public.accounts wa ON wa.id=w.owner_id AND wa.lifecycle='active' AND wa.verification='verified'").
		Where(`c.status='published' AND c.published_at IS NOT NULL AND cv.public_body IS NOT NULL
 AND w.state='published' AND w.draft_deleted_at IS NULL AND w.expires_at>? AND v.moderation_status='approved' AND v.public_body IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM public.penalties p WHERE p.account_id IN(ca.id,wa.id) AND p.revoked_at IS NULL AND p.action_kind IN('mute','ban') AND p.starts_at<=? AND (p.ends_at IS NULL OR p.ends_at>?))
 AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry cr ON cr.id=x.content_id WHERE x.active AND ((cr.kind='wish' AND cr.entity_id=w.id) OR (cr.kind='wish_comment' AND cr.entity_id IN(c.id,c.root_comment_id,c.reply_to_comment_id))))
 AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.status='active' AND ((h.target_kind='wish' AND h.target_id=w.id) OR (h.target_kind='wish_comment' AND h.target_id IN(c.id,c.root_comment_id,c.reply_to_comment_id))))
 AND (c.root_comment_id IS NULL OR EXISTS(SELECT 1 FROM public.wish_comments root JOIN public.accounts ra ON ra.id=root.author_id AND ra.lifecycle='active' WHERE root.id=c.root_comment_id AND root.parent_content_version=c.parent_content_version AND root.status IN('published','withdrawn') AND NOT EXISTS(SELECT 1 FROM public.penalties rp WHERE rp.account_id=ra.id AND rp.revoked_at IS NULL AND rp.action_kind IN('mute','ban') AND rp.starts_at<=clock_timestamp() AND (rp.ends_at IS NULL OR rp.ends_at>clock_timestamp()))))
 `, now, now, now)
}

// WishCommentEligible 在审核结果提交前复核当前父帖、评论上下文及互动双方。
func WishCommentEligible(ctx context.Context, db *gorm.DB, c entity.WishComment, task entity.ModerationTask) (bool, error) {
	var now time.Time
	if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
		return false, e
	}
	var w entity.Wish
	if e := db.WithContext(ctx).Where("id=?", c.WishID).Take(&w).Error; e != nil {
		return false, e
	}
	var author entity.Account
	if e := db.Where("id=?", c.AuthorID).Take(&author).Error; e != nil {
		return false, e
	}
	if c.Status == "withdrawn" || c.Status == "cancelled" || author.Lifecycle != "active" || author.AuthEpoch != task.SubjectEpoch || w.CurrentContentVersion != c.ParentContentVersion || !w.CommentsOpen || w.CommentsEpoch != c.CommentsEpoch || w.State != "published" || w.DraftDeletedAt != nil || w.ExpiresAt == nil || !w.ExpiresAt.After(now) || task.RestrictionEpoch == nil || *task.RestrictionEpoch != w.RestrictionEpoch {
		return false, nil
	}
	var parentValid int64
	if e := db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=? AND moderation_status='approved' AND public_body IS NOT NULL", w.ID, c.ParentContentVersion).Count(&parentValid).Error; e != nil {
		return false, e
	}
	if parentValid != 1 {
		return false, nil
	}
	var epoch int64
	if e := db.Model(&entity.PrivacyHold{}).Select("COALESCE(max(hold_epoch),0)").Where("target_kind='wish' AND target_id=?", w.ID).Scan(&epoch).Error; e != nil {
		return false, e
	}
	if epoch != task.HoldEpoch {
		return false, nil
	}
	var n int64
	e := db.Raw(`SELECT
 (SELECT count(*) FROM public.accounts WHERE id=? AND lifecycle='active' AND verification='verified')-
 (SELECT count(*) FROM public.penalties WHERE account_id IN(?,?) AND revoked_at IS NULL AND action_kind IN('mute','ban') AND starts_at<=? AND (ends_at IS NULL OR ends_at>?))-
 (SELECT count(*) FROM public.blocks WHERE (owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?))-
 (SELECT count(*) FROM public.content_restrictions x JOIN public.content_registry r ON r.id=x.content_id WHERE x.active AND ((r.kind='wish' AND r.entity_id=?) OR (r.kind='wish_comment' AND r.entity_id=?)))-
 (SELECT count(*) FROM public.privacy_holds WHERE status='active' AND ((target_kind='wish' AND target_id=?) OR (target_kind='wish_comment' AND target_id=?)))`, w.OwnerID, w.OwnerID, c.AuthorID, now, now, c.AuthorID, w.OwnerID, w.OwnerID, c.AuthorID, w.ID, c.ID, w.ID, c.ID).Scan(&n).Error
	if e != nil || n != 1 {
		return false, e
	}
	if c.RootCommentID == nil {
		return c.ReplyToCommentID == nil, nil
	}
	if c.ReplyToCommentID == nil {
		return false, nil
	}
	var root, target entity.WishComment
	for _, spec := range []struct {
		id  string
		row *entity.WishComment
	}{{*c.RootCommentID, &root}, {*c.ReplyToCommentID, &target}} {
		// 先用公开查询计数，再按主表加载元数据，避免GORM实体TableName覆盖联表别名。
		if e := visibleWishComments(ctx, db, now).Where("c.id=? AND c.wish_id=?", spec.id, c.WishID).Count(&n).Error; e != nil {
			return false, e
		}
		if n != 1 {
			return false, nil
		}
		if e := db.WithContext(ctx).Where("id=?", spec.id).Take(spec.row).Error; e != nil {
			return false, e
		}
		if e := db.Model(&entity.Block{}).Where("(owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?)", c.AuthorID, spec.row.AuthorID, spec.row.AuthorID, c.AuthorID).Count(&n).Error; e != nil {
			return false, e
		}
		if n > 0 {
			return false, nil
		}
	}
	return root.RootCommentID == nil && root.Kind == "comment" && (target.ID == root.ID || target.RootCommentID != nil && *target.RootCommentID == root.ID), nil
}

// DecryptWishComment 解密绑定精确评论正文版本的原稿。
func DecryptWishComment(ctx context.Context, codec *securedata.Codec, v entity.WishCommentVersion) (out string, err error) {
	var envelope securedata.Envelope
	if json.Unmarshal(v.BodyCipher, &envelope) != nil {
		return "", command.ErrUnavailable
	}
	b, e := codec.Decrypt(ctx, securedata.Binding{Purpose: "wish_comment_body", ObjectID: v.CommentID, Version: uint64(v.ContentVersion)}, envelope)
	defer clear(b)
	if e != nil {
		return "", e
	}
	e = json.Unmarshal(b, &out)
	return out, e
}

// ApplyWishCommentReview 与最终审核决定同事务更新可见正文及去重站内通知。
func ApplyWishCommentReview(ctx context.Context, db *gorm.DB, c entity.WishComment, v entity.WishCommentVersion, decision string, body *string) error {
	status := "rejected"
	if decision == "approved" {
		status = "published"
	}
	if e := db.WithContext(ctx).Model(&entity.WishCommentVersion{}).Where("comment_id=? AND content_version=?", c.ID, c.ContentVersion).Update("public_body", body).Error; e != nil {
		return e
	}
	updates := map[string]any{"status": status, "version": gorm.Expr("version+1"), "updated_at": gorm.Expr("clock_timestamp()"), "published_at": nil}
	if status == "published" {
		updates["published_at"] = gorm.Expr("COALESCE(published_at,clock_timestamp())")
	}
	if e := db.Model(&entity.WishComment{}).Where("id=?", c.ID).Updates(updates).Error; e != nil {
		return e
	}
	if status != "published" {
		return nil
	}
	var w entity.Wish
	if e := db.Where("id=?", c.WishID).Take(&w).Error; e != nil {
		return e
	}
	recipients := []string{w.OwnerID}
	if c.ReplyToCommentID != nil {
		var target entity.WishComment
		if e := db.Where("id=?", *c.ReplyToCommentID).Take(&target).Error; e != nil {
			return e
		}
		recipients = append(recipients, target.AuthorID)
	}
	seen := map[string]bool{}
	for _, recipient := range recipients {
		if recipient == c.AuthorID || seen[recipient] {
			continue
		}
		seen[recipient] = true
		var n int64
		if e := db.Model(&entity.ContentCommentNotice{}).Where("comment_id=? AND recipient_id=?", c.ID, recipient).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			continue
		}
		id := uuid.NewString()
		if e := db.Create(&entity.Notification{ID: id, RecipientID: recipient, Type: "wish_comment", TargetRef: c.WishID}).Error; e != nil {
			return e
		}
		if e := db.Create(&entity.ContentCommentNotice{CommentID: c.ID, RecipientID: recipient, NotificationID: id}).Error; e != nil {
			return e
		}
	}
	return nil
}

// ReadPublicCommentBody 为举报与后台公开素材复用同一条父帖及评论资格查询。
func ReadPublicCommentBody(ctx context.Context, db *gorm.DB, now time.Time, id string) (string, error) {
	var rows []string
	if e := visibleWishComments(ctx, db, now).Where("c.id=?", id).Pluck("cv.public_body", &rows).Error; e != nil {
		return "", e
	}
	if len(rows) != 1 {
		return "", command.ErrNotFound
	}
	return rows[0], nil
}
