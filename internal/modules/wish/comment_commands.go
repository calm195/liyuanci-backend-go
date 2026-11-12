package wish

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/modules/contentreview"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

func validCommentInput(in CommentInput) bool {
	if !utf8.ValidString(in.Body) || strings.TrimSpace(in.Body) == "" || utf8.RuneCountInString(in.Body) > 500 || strings.ContainsRune(in.Body, 0) || in.ExpectedPostVersion < 1 || in.ExpectedCommentsEpoch < 1 {
		return false
	}
	if in.Kind != "comment" && in.Kind != "author_supplement" {
		return false
	}
	return in.RootCommentID == nil && in.ReplyToCommentID == nil || in.Kind == "comment" && in.RootCommentID != nil && in.ReplyToCommentID != nil && validWishID(*in.RootCommentID) && validWishID(*in.ReplyToCommentID)
}
func (s *Service) commentDigest(in any) string {
	b, _ := json.Marshal(in)
	defer clear(b)
	m := hmac.New(sha256.New, s.receiptKey)
	m.Write([]byte("wish.comment.v1\x00"))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

// SaveComment 创建或改稿即送审，未公开原稿只在本人接口回显。
func (s *Service) SaveComment(ctx context.Context, token, key, rid, wishID, id string, in CommentInput) (out Comment, err error) {
	defer func(start time.Time) {
		s.policy.finish("心愿评论操作完成", "wish.comment.save", rid, start, err)
	}(time.Now())
	if in.Kind == "" {
		in.Kind = "comment"
	}
	create := id == ""
	if !validCommentInput(in) || create && !validWishID(wishID) || !create && (!validWishID(id) || in.ExpectedVersion < 1) {
		return out, command.ErrInvalid
	}
	if !create {
		c, _, e := loadWishComment(ctx, s.db, id)
		if e != nil {
			return out, e
		}
		wishID = c.WishID
	}
	method, path := "POST", "/api/v1/wishes/"+wishID+"/comments"
	if !create {
		method, path = "PUT", "/api/v1/me/wish-comments/"+id
	}
	d := command.Definition{Kind: "wish.comment.save", Method: method, ResourcePath: path, ActorKind: "student", Access: command.Ordinary, RetainReceipt: true, RequiredExpectations: []string{"wish_expected_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverWishComment(ctx, db, wishID, id, in.RootCommentID, in.ReplyToCommentID)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		if create {
			return nil
		}
		c, _, e := loadWishComment(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		if c.AuthorID != tx.Actor().ID {
			return command.ErrNotFound
		}
		return nil
	}
	checks := []command.Expectation{{Name: "wish_expected_version", Target: command.Resource{Kind: command.Wish, ID: wishID}, Value: in.ExpectedPostVersion}}
	if !create {
		d.RequiredExpectations = append(d.RequiredExpectations, "expected_version")
		checks = append(checks, command.Expectation{Name: "expected_version", Target: command.Resource{Kind: command.WishComment, ID: id}, Value: in.ExpectedVersion})
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		var w entity.Wish
		if e := db.Where("id=?", wishID).Take(&w).Error; e != nil {
			return "", e
		}
		if in.Kind == "author_supplement" && w.OwnerID != tx.Actor().ID {
			return "", command.ErrForbidden
		}
		c := entity.WishComment{ID: uuid.NewString(), WishID: wishID, AuthorID: tx.Actor().ID, Kind: in.Kind, RootCommentID: in.RootCommentID, ReplyToCommentID: in.ReplyToCommentID, Version: 1, ContentVersion: 1, ParentContentVersion: w.CurrentContentVersion, CommentsEpoch: in.ExpectedCommentsEpoch, Status: "pending_scan", CreatedAt: tx.Now(), UpdatedAt: tx.Now()}
		if !create {
			prior, _, e := loadWishComment(ctx, db, id)
			if e != nil {
				return "", e
			}
			if prior.Kind != in.Kind {
				return "", command.ErrInvalid
			}
			if in.RootCommentID != nil && (prior.RootCommentID == nil || *in.RootCommentID != *prior.RootCommentID || prior.ReplyToCommentID == nil || *in.ReplyToCommentID != *prior.ReplyToCommentID) {
				return "", command.ErrInvalid
			}
			c.ID = id
			c.Version = prior.Version + 1
			c.ContentVersion = prior.ContentVersion + 1
			c.CreatedAt = prior.CreatedAt
			c.RootCommentID = prior.RootCommentID
			c.ReplyToCommentID = prior.ReplyToCommentID
		}
		task := entity.ModerationTask{ID: uuid.NewString(), ContentID: uuid.NewString(), ContentVersion: c.ContentVersion, SubjectEpoch: tx.Actor().AuthEpoch, RestrictionEpoch: &w.RestrictionEpoch, CreatedAt: tx.Now()}
		if e := db.Model(&entity.PrivacyHold{}).Select("COALESCE(max(hold_epoch),0)").Where("target_kind='wish' AND target_id=?", wishID).Scan(&task.HoldEpoch).Error; e != nil {
			return "", e
		}
		if ok, e := WishCommentEligible(ctx, db, c, task); e != nil {
			return "", e
		} else if !ok {
			return "", command.ErrBlockedInteraction
		}
		// 账号guard串行化评论提交频率，持久化回执重放不会再次执行额度检查。
		var recent int64
		if e := db.Model(&entity.WishCommentVersion{}).Where("comment_id IN(SELECT id FROM public.wish_comments WHERE author_id=?) AND created_at>?", tx.Actor().ID, tx.Now().Add(-10*time.Minute)).Count(&recent).Error; e != nil {
			return "", e
		}
		if recent >= 20 {
			return "", command.ErrConflict
		}
		plain, _ := json.Marshal(in.Body)
		defer clear(plain)
		env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "wish_comment_body", ObjectID: c.ID, Version: uint64(c.ContentVersion)}, plain)
		if e != nil {
			return "", e
		}
		cipher, e := json.Marshal(env)
		if e != nil {
			return "", e
		}
		if create {
			if e = db.Create(&c).Error; e != nil {
				return "", e
			}
		} else {
			if e = db.Exec("UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE status IN('pending','human_required','machine_passed') AND content_id IN(SELECT id FROM public.content_registry WHERE kind='wish_comment' AND entity_id=?)", id).Error; e != nil {
				return "", e
			}
			if e = db.Model(&entity.WishComment{}).Where("id=?", id).Updates(map[string]any{"version": c.Version, "content_version": c.ContentVersion, "parent_content_version": c.ParentContentVersion, "comments_epoch": c.CommentsEpoch, "status": "pending_scan", "published_at": nil, "updated_at": tx.Now()}).Error; e != nil {
				return "", e
			}
		}
		if e = db.Create(&entity.ContentRegistry{ID: task.ContentID, Kind: "wish_comment", EntityID: c.ID, AuthorID: c.AuthorID, ContentVersion: c.ContentVersion, SchoolDetectionVersion: "pending"}).Error; e != nil {
			return "", e
		}
		if e = db.Create(&task).Error; e != nil {
			return "", e
		}
		if e = db.Create(&entity.WishCommentVersion{CommentID: c.ID, ContentVersion: c.ContentVersion, ContentRegistryID: task.ContentID, BodyCipher: cipher, ReviewTaskID: task.ID, CreatedAt: tx.Now()}).Error; e != nil {
			return "", e
		}
		if e = durable.Append(ctx, db, s.policy.logger, contentreview.Route(), durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: task.ID, AggregateVersion: 1}); e != nil {
			return "", e
		}
		return c.ID, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.projectComment(ctx, tx.DB(), ref, tx.Actor().ID, true, tx.Now())
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.commentDigest(in), Expectations: checks})
	return
}

// WithdrawComment 允许本人收回已公开或待审评论，根楼保留安全占位的可能性。
func (s *Service) WithdrawComment(ctx context.Context, token, key, rid, id string, version int64) (out Comment, err error) {
	defer func(start time.Time) {
		s.policy.finish("心愿评论操作完成", "wish.comment.withdraw", rid, start, err)
	}(time.Now())
	if !validWishID(id) || version < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "wish.comment.withdraw", Method: "POST", ResourcePath: "/api/v1/me/wish-comments/" + id + "/withdraw", ActorKind: "student", Access: command.Restricted, RetainReceipt: true, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverWishComment(ctx, db, "", id, nil, nil)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		c, _, e := loadWishComment(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		if c.AuthorID != tx.Actor().ID {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		if e := db.Exec("UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE status IN('pending','human_required','machine_passed') AND content_id IN(SELECT id FROM public.content_registry WHERE kind='wish_comment' AND entity_id=?)", id).Error; e != nil {
			return "", e
		}
		return id, db.Model(&entity.WishComment{}).Where("id=?", id).Updates(map[string]any{"status": "withdrawn", "version": gorm.Expr("version+1"), "updated_at": tx.Now()}).Error
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.projectComment(ctx, tx.DB(), ref, tx.Actor().ID, true, tx.Now())
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: version, Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.WishComment, ID: id}, Value: version}}})
	return
}

// SetComments 关闭时取消未公开评论任务；已公开合规评论仍可读取。
func (s *Service) SetComments(ctx context.Context, token, key, rid, id string, version int64, open bool) (out CommentSettings, err error) {
	defer func(start time.Time) {
		s.policy.finish("心愿评论操作完成", "wish.comments.setting", rid, start, err)
	}(time.Now())
	if !validWishID(id) || version < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "wish.comments.setting", Method: "PUT", ResourcePath: "/api/v1/me/wishes/" + id + "/comments-setting", ActorKind: "student", Access: command.Restricted, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return DiscoverWishComment(ctx, db, id, "", nil, nil)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		var n int64
		e := tx.DB().Model(&entity.Wish{}).Where("id=? AND owner_id=? AND draft_deleted_at IS NULL", id, tx.Actor().ID).Count(&n).Error
		if e != nil {
			return e
		}
		if n != 1 {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		if e := db.Model(&entity.Wish{}).Where("id=? AND comments_open<>?", id, open).Updates(map[string]any{"comments_open": open, "comments_epoch": gorm.Expr("comments_epoch+1"), "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		if !open {
			if e := db.Exec("UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE status IN('pending','human_required','machine_passed') AND content_id IN(SELECT r.id FROM public.content_registry r JOIN public.wish_comments c ON r.kind='wish_comment' AND r.entity_id=c.id WHERE c.wish_id=?)", id).Error; e != nil {
				return "", e
			}
			if e := db.Model(&entity.WishComment{}).Where("wish_id=? AND status IN('pending_scan','pending_review')", id).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		return tx.DB().WithContext(ctx).Model(&entity.Wish{}).Select("version,comments_open,comments_epoch").Where("id=?", ref).Scan(&out).Error
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: struct {
		Version int64
		Open    bool
	}{version, open}, Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.Wish, ID: id}, Value: version}}})
	return
}
