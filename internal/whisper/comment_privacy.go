package whisper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// clearHeldComment 通过仅限本人有效会话的函数清除幸存普通副本，不授予API直接改写密文或审核决定的权限。
func (s *Service) clearHeldComment(ctx context.Context, db *gorm.DB, token, id string) error {
	a, e := loadComment(ctx, db, id)
	if e != nil {
		return e
	}
	if !a.privacy && a.comment.PostID != nil {
		return nil
	}
	var versions []entity.WhisperCommentVersion
	if e = db.WithContext(ctx).Where("comment_id=?", id).Find(&versions).Error; e != nil {
		return e
	}
	type cleared struct {
		Version  int64           `json:"content_version"`
		Body     json.RawMessage `json:"body_cipher"`
		Identity json.RawMessage `json:"identity_cipher"`
	}
	rows := []cleared{}
	for _, v := range versions {
		body, e := s.encrypt(ctx, "whisper_comment_body", id, v.ContentVersion, "")
		if e != nil {
			return e
		}
		identity, e := s.encrypt(ctx, "whisper_comment_identity", id, v.ContentVersion, IdentityInput{Mode: "anonymous"})
		if e != nil {
			return e
		}
		rows = append(rows, cleared{v.ContentVersion, body, identity})
	}
	payload, e := json.Marshal(rows)
	if e != nil {
		return e
	}
	defer clear(payload)
	hash := sha256.Sum256([]byte(token))
	return db.WithContext(ctx).Exec("SELECT public.clear_held_whisper_comment(decode(?,'hex'),?,?::jsonb)", hex.EncodeToString(hash[:]), id, string(payload)).Error
}
