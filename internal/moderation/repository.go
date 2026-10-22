package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

type current struct {
	story        *entity.StoryVersion
	message      *entity.Message
	conversation chat.Snapshot
	application  *entity.Application
	task         entity.ModerationTask
	registry     entity.ContentRegistry
	wish         entity.Wish
	content      entity.WishVersion
	account      entity.Account
}

func load(db *gorm.DB, id string) (out current, err error) {
	fail := func(e error) (current, error) {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			e = command.ErrNotFound
		}
		return out, e
	}
	if err = db.Where("id=?", id).Take(&out.task).Error; err != nil {
		return fail(err)
	}
	if err = db.Where("id=? AND kind IN ? AND content_version=?", out.task.ContentID, []string{"wish", "application", "message", "story"}, out.task.ContentVersion).Take(&out.registry).Error; err != nil {
		return fail(err)
	}
	if out.registry.Kind == "story" {
		var v entity.StoryVersion
		if err = db.Where("wish_id=? AND story_version=? AND author_id=?", out.registry.EntityID, out.task.ContentVersion, out.registry.AuthorID).Take(&v).Error; err != nil {
			return fail(err)
		}
		out.story = &v
		if err = db.Where("id=?", v.WishID).Take(&out.wish).Error; err != nil {
			return fail(err)
		}
		if err = db.Where("id=?", v.AuthorID).Take(&out.account).Error; err != nil {
			return fail(err)
		}
		return out, nil
	}
	if out.registry.Kind == "message" {
		var m entity.Message
		if err = db.Where("id=? AND sender_id=? AND content_version=?", out.registry.EntityID, out.registry.AuthorID, out.task.ContentVersion).Take(&m).Error; err != nil {
			return fail(err)
		}
		out.message = &m
		if out.conversation, err = chat.LoadConversation(db.Statement.Context, db, m.ConversationID); err != nil {
			return fail(err)
		}
		out.wish = out.conversation.Wish
		if err = db.Where("id=?", m.SenderID).Take(&out.account).Error; err != nil {
			return fail(err)
		}
		return out, nil
	}
	wishID, author, version := out.registry.EntityID, out.registry.AuthorID, out.task.ContentVersion
	if out.registry.Kind == "application" {
		var app entity.Application
		if err = db.Where("id=? AND applicant_id=? AND content_version=?", out.registry.EntityID, out.registry.AuthorID, out.task.ContentVersion).Take(&app).Error; err != nil {
			return fail(err)
		}
		out.application = &app
		wishID = app.WishID
	}
	if err = db.Where("id=?", wishID).Take(&out.wish).Error; err != nil {
		return fail(err)
	}
	if out.application != nil {
		version = out.wish.CurrentContentVersion
	} else if out.wish.OwnerID != author {
		return out, command.ErrNotFound
	}
	if err = db.Where("wish_id=? AND content_version=?", wishID, version).Take(&out.content).Error; err != nil {
		return fail(err)
	}
	if err = db.Where("id=?", author).Take(&out.account).Error; err != nil {
		return fail(err)
	}
	return out, nil
}
func discover(ctx context.Context, db *gorm.DB, id string) (command.Discovery, error) {
	row, e := load(db.WithContext(ctx), id)
	if errors.Is(e, command.ErrNotFound) {
		return command.Discovery{}, nil
	}
	if e != nil {
		return command.Discovery{}, e
	}
	if row.story != nil {
		return wish.DiscoverStory(ctx, db, row.wish.ID)
	}
	if row.message != nil {
		p, e := chat.DiscoverConversation(ctx, db, row.message.ConversationID)
		if e != nil {
			return p, e
		}
		p.Resources = append(p.Resources, command.Resource{Kind: command.ModerationTask, ID: id}, command.Resource{Kind: command.Message, ID: row.message.ID})
		return p, nil
	}
	if row.application != nil {
		return command.Discovery{Accounts: []string{row.account.ID, row.wish.OwnerID}, Resources: []command.Resource{{Kind: command.Wish, ID: row.wish.ID}, {Kind: command.Application, ID: row.application.ID}, {Kind: command.ModerationTask, ID: id}}}, nil
	}

	return command.Discovery{Accounts: []string{row.account.ID}, Resources: []command.Resource{{Kind: command.Wish, ID: row.wish.ID}, {Kind: command.ModerationTask, ID: id}}}, nil
}

func currentBinding(row current) bool {
	if row.story != nil {
		return row.wish.CurrentStoryVersion == row.task.ContentVersion && row.story.ReviewTaskID != nil && *row.story.ReviewTaskID == row.task.ID
	}
	if row.message != nil {
		return row.message.ReviewTaskID != nil && *row.message.ReviewTaskID == row.task.ID && row.message.ContentVersion == row.task.ContentVersion
	}
	if row.application != nil {
		return row.application.WishContentVersion == row.wish.CurrentContentVersion && row.application.ReviewTaskID != nil && *row.application.ReviewTaskID == row.task.ID && row.application.ContentVersion == row.task.ContentVersion
	}
	return row.wish.CurrentContentVersion == row.task.ContentVersion && row.content.ReviewTaskID != nil && *row.content.ReviewTaskID == row.task.ID
}

// eligible 在账号和资源锁后复核当前任务绑定、资格、处罚及全部历史版本限制，不从旧事件恢复正文。
func eligible(db *gorm.DB, row current) (bool, error) {
	if row.story != nil {
		return wish.StoryEligible(db.Statement.Context, db, row.wish, *row.story, row.task)
	}
	if row.message != nil {
		if !currentBinding(row) {
			return false, nil
		}
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return false, e
		}
		return chat.EligibleMessage(db.Statement.Context, db, *row.message, now)
	}
	if row.application != nil {
		if !currentBinding(row) {
			return false, nil
		}
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return false, e
		}
		return wish.ApplicationEligible(db.Statement.Context, db, *row.application, now)
	}
	if row.account.Lifecycle != "active" || row.account.Verification != "verified" || row.wish.DraftDeletedAt != nil || row.wish.CurrentContentVersion != row.task.ContentVersion || row.content.ReviewTaskID == nil || *row.content.ReviewTaskID != row.task.ID || row.task.SubjectEpoch != row.account.AuthEpoch || row.task.RestrictionEpoch == nil || *row.task.RestrictionEpoch != row.wish.RestrictionEpoch || row.wish.State != "pending_review" {
		return false, nil
	}
	var n int64
	if e := db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ? AND revoked_at IS NULL AND starts_at<=clock_timestamp() AND (ends_at IS NULL OR ends_at>clock_timestamp())", row.account.ID, []string{"mute", "ban"}).Count(&n).Error; e != nil {
		return false, e
	}
	if n > 0 {
		return false, nil
	}
	if e := db.Model(&entity.ContentRestriction{}).Table("public.content_restrictions AS r").Joins("JOIN public.content_registry c ON c.id=r.content_id").Where("c.kind='wish' AND c.entity_id=? AND r.active", row.wish.ID).Count(&n).Error; e != nil {
		return false, e
	}
	if n > 0 {
		return false, nil
	}
	var holds []entity.PrivacyHold
	if e := db.Where("target_kind='wish' AND target_id=?", row.wish.ID).Find(&holds).Error; e != nil {
		return false, e
	}
	var epoch int64
	for _, hold := range holds {
		if hold.HoldEpoch > epoch {
			epoch = hold.HoldEpoch
		}
		if hold.Status == "active" {
			return false, nil
		}
	}
	return epoch == row.task.HoldEpoch, nil
}
func setReview(db *gorm.DB, codec *securedata.Codec, row current, decision string, body *string, tags []byte) error {
	if row.story != nil {
		if e := db.Model(&entity.ContentRegistry{}).Where("id=?", row.registry.ID).Update("status", decision).Error; e != nil {
			return e
		}
		return wish.ApplyStoryReview(db.Statement.Context, db, row.wish, *row.story, row.task, decision, body, tags)
	}
	if row.message != nil {
		if decision == "approved" {
			var now time.Time
			if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
				return e
			}
			if e := chat.Deliver(db.Statement.Context, db, *row.message, now); e != nil {
				return e
			}
		} else {
			if e := db.Model(&entity.Message{}).Where("id=?", row.message.ID).Updates(map[string]any{"status": "rejected", "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			if e := db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: row.message.SenderID, Type: "message_reviewed", TargetRef: row.message.ID}).Error; e != nil {
				return e
			}
		}
		return db.Model(&entity.ContentRegistry{}).Where("id=?", row.registry.ID).Update("status", decision).Error
	}
	if e := db.Model(&entity.ContentRegistry{}).Where("id=?", row.registry.ID).Update("status", decision).Error; e != nil {
		return e
	}
	if row.application != nil {
		app := row.application
		status := "rejected"
		var cipher any
		if decision == "approved" {
			if body == nil || !candidateBody(row, *body) {
				return command.ErrInvalid
			}
			data, e := json.Marshal(*body)
			if e != nil {
				return e
			}
			defer clear(data)
			encrypted, e := codec.Encrypt(db.Statement.Context, securedata.Binding{Purpose: "application_approved", ObjectID: app.ID, Version: uint64(app.ContentVersion)}, data)
			if e != nil {
				return e
			}
			raw, e := json.Marshal(encrypted)
			if e != nil {
				return e
			}
			cipher = json.RawMessage(raw)
			status = "active"
		}
		if e := db.Model(&entity.Application{}).Where("id=?", app.ID).Updates(map[string]any{"status": status, "public_message_cipher": cipher, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		if status == "active" {
			return db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: row.wish.OwnerID, Type: "application_active", TargetRef: app.ID}).Error
		}
		return nil
	}
	if e := db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", row.wish.ID, row.task.ContentVersion).Updates(map[string]any{"moderation_status": decision, "public_body": body, "identity_tags": json.RawMessage(tags)}).Error; e != nil {
		return e
	}
	state := "ready"
	if decision == "rejected" {
		state = "rejected"
	}
	return db.Model(&entity.Wish{}).Where("id=?", row.wish.ID).Updates(map[string]any{"state": state, "version": gorm.Expr("version+1")}).Error
}
