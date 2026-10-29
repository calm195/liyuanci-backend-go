package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// captureWhisperCrisis 将精确原文及最小学号证据与隔离同事务提交；未实名主体不伪造身份。
func captureWhisperCrisis(ctx context.Context, db *gorm.DB, codec *securedata.Codec, r whisperSource, now time.Time, capturedBody ...string) (string, error) {
	var old entity.EvidenceCase
	e := db.Where("kind='crisis' AND source_ref=?", r.registry.ID).Take(&old).Error
	if e == nil {
		var c entity.EvidenceCrisisCase
		e = db.Where("evidence_case_id=?", old.ID).Take(&c).Error
		return c.ID, e
	}
	if !errors.Is(e, gorm.ErrRecordNotFound) {
		return "", e
	}
	if !whisperCurrent(r) {
		return "", command.ErrConflict
	}
	var body string
	if len(capturedBody) > 0 {
		body = capturedBody[0]
	} else {
		body, _, e = whisperDecrypt(ctx, db, codec, r)
		if e != nil {
			return "", e
		}
	}
	subject := crisisSubject{AccountID: r.OwnerID}
	identityStatus := "not_verified"
	if r.account.Verification == "verified" {
		var secrets []entity.IdentitySecret
		if e = db.Where("account_id=? AND purpose='student_number'", r.OwnerID).Find(&secrets).Error; e != nil {
			return "", e
		}
		if len(secrets) != 1 {
			return "", command.ErrUnavailable
		}
		var env securedata.Envelope
		if json.Unmarshal(secrets[0].Envelope, &env) != nil {
			return "", command.ErrUnavailable
		}
		b, e := codec.Decrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: r.OwnerID, Version: uint64(secrets[0].ContentVersion)}, env)
		if e != nil {
			return "", e
		}
		subject.StudentNumber = string(b)
		clear(b)
		if subject.StudentNumber == "" {
			return "", command.ErrUnavailable
		}
		identityStatus = "verified"
	}
	caseID, itemID, scopeID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	cipher, key, e := encryptEvidence(ctx, codec, "crisis_body", itemID, body)
	if e != nil {
		return "", e
	}
	identity, _, e := encryptEvidence(ctx, codec, "crisis_subject", itemID, subject)
	if e != nil {
		return "", e
	}
	handoff := HandoffAt(now)
	for _, v := range []any{&entity.EvidenceCase{ID: scopeID, Kind: "crisis", SourceRef: r.registry.ID, Status: "open", CreatedAt: now}, &entity.EvidenceCrisisCase{ID: caseID, EvidenceCaseID: scopeID, ContentType: r.registry.Kind, Status: "pending_handling", DetectedAt: now, Version: 1, SourceTaskID: &r.task.ID, HandoffAt: &handoff, IdentityStatus: &identityStatus, SourceEntityID: &r.ID, SourceContentVersion: &r.ContentVersion}, &entity.EvidenceItem{ID: itemID, CaseID: scopeID, SubjectCipher: identity, BodyCipher: cipher, KeyRef: key, CreatedAt: now}, &entity.ContentRestriction{ID: uuid.NewString(), ContentID: r.registry.ID, Kind: "crisis", SourceRef: caseID, Active: true, Version: 1}, &entity.CrisisDelivery{ID: uuid.NewString(), CaseID: caseID, Kind: "care", Status: "pending", NextAt: now}, &entity.CrisisDelivery{ID: uuid.NewString(), CaseID: caseID, Kind: "operator", Status: "pending", NextAt: now}} {
		if e = db.Create(v).Error; e != nil {
			return "", e
		}
	}
	if e = hideWhisper(db, r, true, now); e != nil {
		return "", e
	}
	return caseID, nil
}

func hideWhisper(db *gorm.DB, r whisperSource, advance bool, now time.Time) error {
	u := map[string]any{"version": gorm.Expr("version+1")}
	if advance {
		u["restriction_epoch"] = gorm.Expr("restriction_epoch+1")
	}
	switch r.registry.Kind {
	case "whisper_post":
		u["published_at"] = nil
		u["published_content_version"] = nil
		u["comments_epoch"] = gorm.Expr("comments_epoch+1")
		if e := db.Model(&entity.WhisperPostVersion{}).Where("post_id=?", r.ID).Updates(map[string]any{"public_body": nil, "public_identity": nil}).Error; e != nil {
			return e
		}
		return db.Model(&entity.WhisperPost{}).Where("id=?", r.ID).Updates(u).Error
	case "whisper_comment":
		u["published_at"] = nil
		if e := db.Model(&entity.WhisperCommentVersion{}).Where("comment_id=?", r.ID).Updates(map[string]any{"public_body": nil, "public_identity": nil}).Error; e != nil {
			return e
		}
		return db.Model(&entity.WhisperComment{}).Where("id=?", r.ID).Updates(u).Error
	case "whisper_nickname":
		return db.Model(&entity.WhisperNicknameVersion{}).Where("content_registry_id=?", r.ID).Updates(map[string]any{"public_label": nil, "revoked_at": now, "control_version": gorm.Expr("control_version+1")}).Error
	case "whisper_capsule":
		return db.Model(&entity.WhisperCapsule{}).Where("id=?", r.ID).Updates(u).Error
	}
	return command.ErrNotFound
}
