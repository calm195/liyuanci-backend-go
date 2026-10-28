package whisper

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// SaveComment 创建即送审，改文立即隐藏旧投影并绑定当前父帖上下文。
func (s *Service) SaveComment(ctx context.Context, token, key, rid, postID, commentID string, in CommentInput) (out OwnerComment, err error) {
	defer s.completed("comment_save", rid, time.Now(), &err)
	create := commentID == ""
	if (!create && (!ValidID(commentID) || in.ExpectedVersion < 1)) || (create && !ValidID(postID)) || in.ExpectedPostVersion < 1 || in.ExpectedCommentsEpoch < 1 || !validText(in.Body, 1, 500) || !identityShape(in.Identity) {
		return out, command.ErrInvalid
	}
	method, path, kind := "PUT", "/api/v1/me/whispers/comments/"+commentID, "whisper.comment.update"
	if create {
		method, path, kind = "POST", "/api/v1/whispers/posts/"+postID+"/comments", "whisper.comment.create"
	}
	d := command.Definition{Kind: kind, Method: method, ResourcePath: path, ActorKind: "student", Access: command.Ordinary, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return interactionPlan(ctx, db, postID, commentID)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		if create {
			return nil
		}
		a, e := loadComment(ctx, tx.DB(), commentID)
		if e != nil {
			return e
		}
		if a.comment.AuthorID != tx.Actor().ID {
			return command.ErrNotFound
		}
		if a.comment.PostID == nil {
			return command.ErrConflict
		}
		postID = *a.comment.PostID
		return nil
	}
	// 父帖引用在发现阶段已固定；命令期望同时校验父资源和评论资源。
	if !create {
		var c entity.WhisperComment
		if e := s.db.WithContext(ctx).Select("post_id").Where("id=?", commentID).Take(&c).Error; e != nil {
			return out, missingRow(e)
		}
		if c.PostID == nil {
			return out, command.ErrConflict
		}
		postID = *c.PostID
	}
	checks := []command.Expectation{{Name: "expected_post_version", Target: command.Resource{Kind: command.WhisperPost, ID: postID}, Value: in.ExpectedPostVersion}}
	d.RequiredExpectations = []string{"expected_post_version"}
	if !create {
		d.RequiredExpectations = append(d.RequiredExpectations, "expected_version")
		checks = append(checks, command.Expectation{Name: "expected_version", Target: command.Resource{Kind: command.WhisperComment, ID: commentID}, Value: in.ExpectedVersion})
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		parent, e := s.commentContext(ctx, tx.DB(), tx.Now(), postID, tx.Actor().ID, in.ExpectedCommentsEpoch)
		if e != nil {
			return "", e
		}
		if e = s.validateIdentity(ctx, tx.DB(), tx.Actor().ID, in.Identity); e != nil {
			return "", e
		}
		id, cv, round, hold, restriction := commentID, int64(1), int64(1), int64(0), int64(0)
		if create {
			id = uuid.NewString()
			if e = insertRow(ctx, tx.DB(), &entity.WhisperComment{ID: id, PostID: &postID, AuthorID: tx.Actor().ID, ParentContentVersion: parent.post.CurrentContentVersion, CommentsEpoch: in.ExpectedCommentsEpoch, ReviewRound: round, CreatedAt: tx.Now(), UpdatedAt: tx.Now()}); e != nil {
				return "", e
			}
		} else {
			a, e := loadComment(ctx, tx.DB(), id)
			if e != nil {
				return "", e
			}
			if a.privacy || a.isolated || a.restricted {
				return "", command.ErrConflict
			}
			cv, round, hold, restriction = a.comment.ContentVersion+1, a.comment.ReviewRound+1, a.comment.PrivacyEpoch, a.comment.RestrictionEpoch
			if e = cancelCommentReviews(ctx, tx.DB(), id); e != nil {
				return "", e
			}
			if e = tx.DB().WithContext(ctx).Model(&entity.WhisperComment{}).Where("id=?", id).Updates(map[string]any{"version": a.comment.Version + 1, "content_version": cv, "parent_content_version": parent.post.CurrentContentVersion, "comments_epoch": in.ExpectedCommentsEpoch, "review_state": "scanning", "review_round": round, "lifecycle": "active", "updated_at": tx.Now(), "published_at": nil}).Error; e != nil {
				return "", e
			}
		}
		label, e := randomAlias()
		if e != nil {
			return "", e
		}
		if e = tx.DB().WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entity.WhisperAlias{PostID: postID, ActorID: tx.Actor().ID, Label: label}).Error; e != nil {
			return "", e
		}
		body, e := s.encrypt(ctx, "whisper_comment_body", id, cv, in.Body)
		if e != nil {
			return "", e
		}
		identity, e := s.encrypt(ctx, "whisper_comment_identity", id, cv, in.Identity)
		if e != nil {
			return "", e
		}
		reg := uuid.NewString()
		if e = registry(ctx, tx.DB(), reg, "whisper_comment", id, tx.Actor().ID, cv); e != nil {
			return "", e
		}
		task, e := s.submit(ctx, tx, reg, "whisper_comment", cv, round, hold, restriction)
		if e != nil {
			return "", e
		}
		return id, insertRow(ctx, tx.DB(), &entity.WhisperCommentVersion{CommentID: id, ContentVersion: cv, ContentRegistryID: reg, BodyCipher: body, IdentityCipher: identity, IdentityMode: in.Identity.Mode, NicknameVersion: in.Identity.NicknameVersion, DictionaryVersion: in.Identity.DictionaryVersion, ReviewTaskID: &task, CreatedAt: tx.Now()})
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.ownerComment(ctx, tx.DB(), tx.Now(), ref, tx.Actor().ID)
		return e
	}
	_, err = s.execute(ctx, d, token, key, rid, in, 0, "", "comment", checks...)
	return
}

func cancelCommentReviews(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Exec(`UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE status IN ('pending','machine_passed','human_required') AND content_id IN (SELECT id FROM public.content_registry WHERE kind='whisper_comment' AND entity_id=?)`, id).Error
}

// WithdrawComment 允许受罚本人撤回；回执只重查当前合法本人投影。
func (s *Service) WithdrawComment(ctx context.Context, token, key, rid, id string, version int64) (out OwnerComment, err error) {
	defer s.completed("comment_withdraw", rid, time.Now(), &err)
	if !ValidID(id) || version < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "whisper.comment.withdraw", Method: "POST", ResourcePath: "/api/v1/me/whispers/comments/" + id + "/withdraw", ActorKind: "student", Access: command.Restricted, RetainReceipt: true, RequiredExpectations: []string{"expected_version"}}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return interactionPlan(ctx, db, "", id)
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		a, e := loadComment(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		if a.comment.AuthorID != tx.Actor().ID {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		if e := cancelCommentReviews(ctx, tx.DB(), id); e != nil {
			return "", e
		}
		e := tx.DB().WithContext(ctx).Model(&entity.WhisperComment{}).Where("id=?", id).Updates(map[string]any{"version": gorm.Expr("version+1"), "lifecycle": "withdrawn", "updated_at": tx.Now()}).Error
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.ownerComment(ctx, tx.DB(), tx.Now(), ref, tx.Actor().ID)
		return e
	}
	_, err = s.execute(ctx, d, token, key, rid, struct{ Version int64 }{version}, 0, "", "maintenance", command.Expectation{Name: "expected_version", Target: command.Resource{Kind: command.WhisperComment, ID: id}, Value: version})
	return
}

// SetComments 改变开关时递增评论代次，只取消待审项，不撤销既有批准版本。
func (s *Service) SetComments(ctx context.Context, token, key, rid, id string, version int64, open bool) (out OwnerPost, err error) {
	defer s.completed("comments_setting", rid, time.Now(), &err)
	if !ValidID(id) || version < 1 {
		return out, command.ErrInvalid
	}
	d := s.definition("whisper.comments.setting", "PUT", "/api/v1/me/whispers/posts/"+id+"/comments-setting", id, true)
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		var p entity.WhisperPost
		if e := tx.DB().WithContext(ctx).Where("id=?", id).Take(&p).Error; e != nil {
			return "", e
		}
		if p.CommentsOpen == open {
			return id, nil
		}
		if e := tx.DB().WithContext(ctx).Exec(`UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE status IN ('pending','machine_passed','human_required') AND content_id IN (SELECT r.id FROM public.content_registry r JOIN public.whisper_comments c ON c.id=r.entity_id WHERE r.kind='whisper_comment' AND c.post_id=?)`, id).Error; e != nil {
			return "", e
		}
		return id, updatePost(ctx, tx.DB(), id, map[string]any{"comments_open": open, "comments_epoch": p.CommentsEpoch + 1, "version": p.Version + 1, "updated_at": tx.Now()})
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.ownerPost(ctx, tx.DB(), tx.Now(), ref, tx.Actor().ID)
		return e
	}
	_, err = s.execute(ctx, d, token, key, rid, struct {
		Version int64
		Open    bool
	}{version, open}, version, id, "maintenance")
	return
}
