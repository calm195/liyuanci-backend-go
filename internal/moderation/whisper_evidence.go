package moderation

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// WhisperEvidenceScope 只供超管删除确认，不包含证据正文、主体或对象地址。
type WhisperEvidenceScope struct {
	ID             string   `json:"id"`
	Version        int64    `json:"version"`
	State          string   `json:"state"`
	AllowedActions []string `json:"allowed_actions"`
}

// WhisperEvidence 只接受树洞危机证据范围ID，拒绝普通证据项的宽松别名。
func (s *Service) WhisperEvidence(ctx context.Context, token, rid, id string) (out WhisperEvidenceScope, err error) {
	defer s.done("whisper.evidence.scope", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		if _, e := s.authorizeRoles(ctx, db, token, command.Discovery{}, []string{"super_admin"}); e != nil {
			return e
		}
		var c entity.EvidenceCase
		if e := db.Where("id=? AND EXISTS(SELECT 1 FROM evidence.crisis_cases x WHERE x.evidence_case_id=evidence_cases.id AND x.content_type IN ('whisper_post','whisper_comment','whisper_nickname','whisper_capsule'))", id).Take(&c).Error; e != nil {
			return missingWhisper(e)
		}
		out = WhisperEvidenceScope{c.ID, c.Version, "retained", []string{"whisperAdminEvidenceDelete"}}
		if c.Status == "deletion_pending" {
			out.State = "deleting"
			out.AllowedActions = []string{}
		}
		return nil
	})
	if err != nil {
		out = WhisperEvidenceScope{}
	}
	return
}
