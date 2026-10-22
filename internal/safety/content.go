package safety

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// caseBody 仅返回绑定案件的精确允许视图，不包含作者完整身份或其他聊天记录。
// 主体注销、内容已换版或隐私屏障生效后不再返回旧正文；案件技术元数据仍可处置。
func (s *Service) caseBody(ctx context.Context, db *gorm.DB, c entity.ContentRegistry) (string, error) {
	var n int64
	if e := db.Model(&entity.Account{}).Where("id=? AND lifecycle='active'", c.AuthorID).Count(&n).Error; e != nil {
		return "", e
	}
	if n != 1 {
		return "", nil
	}
	current, e := targetRegistry(db, c.Kind, c.EntityID)
	if e != nil {
		return "", e
	}
	if current.ID != c.ID {
		return "", nil
	}
	switch c.Kind {
	case "wish", "story":
		if e = db.Model(&entity.Wish{}).Where("id=? AND draft_deleted_at IS NULL", c.EntityID).Count(&n).Error; e != nil {
			return "", e
		}
		if n != 1 {
			return "", nil
		}
		if e = db.Model(&entity.PrivacyHold{}).Where("target_id=? AND status='active'", c.EntityID).Count(&n).Error; e != nil {
			return "", e
		}
		if n > 0 {
			return "", nil
		}
		if c.Kind == "wish" {
			var v entity.WishVersion
			if e = db.Where("wish_id=? AND content_version=?", c.EntityID, c.ContentVersion).Take(&v).Error; e != nil {
				return "", e
			}
			if v.PublicBody != nil {
				return *v.PublicBody, nil
			}
		} else {
			var v entity.StoryVersion
			if e = db.Where("wish_id=? AND story_version=?", c.EntityID, c.ContentVersion).Take(&v).Error; e != nil {
				return "", e
			}
			if v.PublicBody != nil {
				return *v.PublicBody, nil
			}
		}
	case "application":
		var a entity.Application
		if e = db.Where("id=?", c.EntityID).Take(&a).Error; e != nil {
			return "", e
		}
		if len(a.PublicMessageCipher) == 0 {
			return "", nil
		}
		var w entity.Wish
		if e = db.Where("id=?", a.WishID).Take(&w).Error; e != nil {
			return "", e
		}
		if w.DraftDeletedAt != nil {
			return "", nil
		}
		if e = db.Model(&entity.PrivacyHold{}).Where("target_id=? AND status='active'", w.ID).Count(&n).Error; e != nil {
			return "", e
		}
		if n > 0 {
			return "", nil
		}
		var env securedata.Envelope
		if json.Unmarshal(a.PublicMessageCipher, &env) != nil {
			return "", command.ErrUnavailable
		}
		raw, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "application_approved", ObjectID: a.ID, Version: uint64(a.ContentVersion)}, env)
		defer clear(raw)
		if e != nil {
			return "", e
		}
		var body string
		e = json.Unmarshal(raw, &body)
		return body, e
	case "message":
		var m entity.Message
		if e = db.Where("id=?", c.EntityID).Take(&m).Error; e != nil {
			return "", e
		}
		if m.Status != "delivered" {
			return "", nil
		}
		v, e := chat.LoadConversation(ctx, db, m.ConversationID)
		if e != nil {
			return "", e
		}
		if !v.CanRead(m.SenderID) {
			return "", nil
		}
		return chat.DecryptBody(ctx, s.codec, m)
	}
	return "", nil
}
