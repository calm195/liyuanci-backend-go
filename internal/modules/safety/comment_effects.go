package safety

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type pendingCommentRelation struct {
	ID, AuthorID, ParentID, ParentAuthorID, TaskID string
	RootCommentID, ReplyToCommentID                *string
	RootAuthorID, ReplyAuthorID                    *string
}

// pendingCommentRelations 只读取关系元数据；审核角色无须获得评论原稿权限。
func pendingCommentRelations(ctx context.Context, db *gorm.DB, kind string, accounts []string) ([]pendingCommentRelation, error) {
	comments, parents, parentKey := "public.wish_comments", "public.wishes", "wish_id"
	if kind == "whisper_comment" {
		comments, parents, parentKey = "public.whisper_comments", "public.whisper_posts", "post_id"
	}
	var rows []pendingCommentRelation
	err := db.WithContext(ctx).Table(comments+" AS c").
		Select("c.id,c.author_id,p.id AS parent_id,p.owner_id AS parent_author_id,c.root_comment_id,c.reply_to_comment_id,root.author_id AS root_author_id,reply.author_id AS reply_author_id,t.id AS task_id").
		Joins("JOIN "+parents+" p ON p.id=c."+parentKey).
		Joins("LEFT JOIN "+comments+" root ON root.id=c.root_comment_id").
		Joins("LEFT JOIN "+comments+" reply ON reply.id=c.reply_to_comment_id").
		Joins("JOIN public.content_registry r ON r.entity_id=c.id AND r.kind=?", kind).
		Joins("JOIN public.moderation_tasks t ON t.content_id=r.id AND t.status IN ('pending','machine_passed','human_required')").
		Where("c.author_id IN ? OR p.owner_id IN ? OR root.author_id IN ? OR reply.author_id IN ?", accounts, accounts, accounts, accounts).
		Scan(&rows).Error
	return rows, err
}

// commentEffectsPlan 与新评论、改稿和审核共享所有参与账号与资源锁，取消集合发生变化时由命令框架重试。
func commentEffectsPlan(ctx context.Context, db *gorm.DB, accounts []string) (p command.Discovery, err error) {
	for _, kind := range []string{"wish_comment", "whisper_comment"} {
		rows, e := pendingCommentRelations(ctx, db, kind, accounts)
		if e != nil {
			return p, e
		}
		parentKind, commentKind := command.Wish, command.WishComment
		if kind == "whisper_comment" {
			parentKind, commentKind = command.WhisperPost, command.WhisperComment
		}
		for _, row := range rows {
			p.Accounts = append(p.Accounts, row.AuthorID, row.ParentAuthorID)
			p.Resources = append(p.Resources,
				command.Resource{Kind: parentKind, ID: row.ParentID},
				command.Resource{Kind: commentKind, ID: row.ID},
				command.Resource{Kind: command.ModerationTask, ID: row.TaskID},
			)
			for _, ref := range []*string{row.RootCommentID, row.ReplyToCommentID} {
				if ref != nil {
					p.Resources = append(p.Resources, command.Resource{Kind: commentKind, ID: *ref})
				}
			}
			for _, author := range []*string{row.RootAuthorID, row.ReplyAuthorID} {
				if author != nil {
					p.Accounts = append(p.Accounts, *author)
				}
			}
		}
	}
	return p, nil
}

func commentRelationAffected(row pendingCommentRelation, a, b string) bool {
	participants := []string{row.ParentAuthorID}
	for _, ref := range []*string{row.RootAuthorID, row.ReplyAuthorID} {
		if ref != nil {
			participants = append(participants, *ref)
		}
	}
	if b == "" && row.AuthorID == a {
		return true
	}
	for _, participant := range participants {
		if b == "" && participant == a ||
			row.AuthorID == a && participant == b || row.AuthorID == b && participant == a {
			return true
		}
	}
	return false
}

// cancelCommentRelationships 永久取消旧待审轮次；解除屏蔽只能通过新的显式改稿再次审核。
func cancelCommentRelationships(ctx context.Context, db *gorm.DB, a, b string) error {
	for _, kind := range []string{"wish_comment", "whisper_comment"} {
		rows, e := pendingCommentRelations(ctx, db, kind, []string{a})
		if e != nil {
			return e
		}
		var ids, tasks []string
		for _, row := range rows {
			if commentRelationAffected(row, a, b) {
				ids = append(ids, row.ID)
				tasks = append(tasks, row.TaskID)
			}
		}
		if len(tasks) == 0 {
			continue
		}
		if e = db.WithContext(ctx).Model(&entity.ModerationTask{}).Where("id IN ? AND status IN ('pending','machine_passed','human_required')", tasks).
			Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		// 暖评状态由当前任务投影；心愿评论另有持久状态，必须同时终止以避免长期显示待审。
		if kind == "wish_comment" {
			if e = db.WithContext(ctx).Model(&entity.WishComment{}).Where("id IN ? AND status IN ('pending_scan','pending_review')", ids).
				Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
		}
	}
	return nil
}
