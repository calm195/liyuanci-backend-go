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

// HandoffAt 返回北京时间8至22时窗口起点，夜间顺延至下一次8时，不承诺响应时限。
func HandoffAt(now time.Time) time.Time {
	local := now.In(time.FixedZone("Asia/Shanghai", 8*60*60))
	if local.Hour() < 8 {
		return time.Date(local.Year(), local.Month(), local.Day(), 8, 0, 0, 0, local.Location()).UTC()
	}
	if local.Hour() >= 22 {
		return time.Date(local.Year(), local.Month(), local.Day()+1, 8, 0, 0, 0, local.Location()).UTC()
	}
	return now.UTC()
}

func encryptEvidence(ctx context.Context, codec *securedata.Codec, purpose, id string, value any) (json.RawMessage, string, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return nil, "", e
	}
	defer clear(b)
	env, e := codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: 1}, b)
	if e != nil {
		return nil, "", e
	}
	raw, e := json.Marshal(env)
	return raw, env.KeyRef, e
}
func decryptEvidence(ctx context.Context, codec *securedata.Codec, purpose, id string, b json.RawMessage, out any) error {
	var env securedata.Envelope
	if json.Unmarshal(b, &env) != nil {
		return command.ErrUnavailable
	}
	plain, e := codec.Decrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: 1}, env)
	if e != nil {
		return e
	}
	defer clear(plain)
	return json.Unmarshal(plain, out)
}

type crisisSubject struct {
	AccountID     string
	StudentNumber string
}

// capturable 拒绝已取消、撤回、换版或删除的普通内容升格为永久证据。
func capturable(row current) bool {
	if row.account.Lifecycle != "active" || !currentBinding(row) || row.wish.DraftDeletedAt != nil || row.task.Status == "obsolete" || row.task.Status == "cancelled" {
		return false
	}
	if row.message != nil && row.message.Status == "cancelled" {
		return false
	}
	if row.application != nil && (row.application.Status == "withdrawn" || row.application.Status == "invalidated") {
		return false
	}
	return row.story == nil || row.story.WithdrawnAt == nil
}

// captureCrisis 只保留该精确版本及作者学号；没有复制学生卡、对端身份或其他私聊。
// 调用方已持账号及领域锁，证据、隔离和投递待办随原消费事务一起提交。
func captureCrisis(ctx context.Context, db *gorm.DB, codec *securedata.Codec, row current, now time.Time) (string, error) {
	var old entity.EvidenceCase
	e := db.Where("kind='crisis' AND source_ref=?", row.registry.ID).Take(&old).Error
	if e == nil {
		var c entity.EvidenceCrisisCase
		e = db.Where("evidence_case_id=?", old.ID).Take(&c).Error
		return c.ID, e
	}
	if !errors.Is(e, gorm.ErrRecordNotFound) {
		return "", e
	}
	if !capturable(row) {
		return "", command.ErrConflict
	}
	input, e := decryptContent(ctx, codec, row)
	if e != nil {
		return "", e
	}
	subject := crisisSubject{AccountID: row.account.ID}
	var secrets []entity.IdentitySecret
	if e = db.Where("account_id=? AND purpose='student_number'", row.account.ID).Find(&secrets).Error; e != nil {
		return "", e
	}
	if len(secrets) == 1 {
		var env securedata.Envelope
		if json.Unmarshal(secrets[0].Envelope, &env) != nil {
			return "", command.ErrUnavailable
		}
		b, e := codec.Decrypt(ctx, securedata.Binding{Purpose: "student_number", ObjectID: row.account.ID, Version: uint64(secrets[0].ContentVersion)}, env)
		if e != nil {
			return "", e
		}
		subject.StudentNumber = string(b)
		clear(b)
	}
	caseID, itemID, evidenceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	body, key, e := encryptEvidence(ctx, codec, "crisis_body", itemID, input.Body)
	if e != nil {
		return "", e
	}
	identity, _, e := encryptEvidence(ctx, codec, "crisis_subject", itemID, subject)
	if e != nil {
		return "", e
	}
	handoff := HandoffAt(now)
	for _, v := range []any{
		&entity.EvidenceCase{ID: evidenceID, Kind: "crisis", SourceRef: row.registry.ID, Status: "open", CreatedAt: now},
		&entity.EvidenceCrisisCase{ID: caseID, EvidenceCaseID: evidenceID, ContentType: row.registry.Kind, Status: "pending_handling", DetectedAt: now, Version: 1, SourceTaskID: &row.task.ID, HandoffAt: &handoff},
		&entity.EvidenceItem{ID: itemID, CaseID: evidenceID, SubjectCipher: identity, BodyCipher: body, KeyRef: key, CreatedAt: now},
		&entity.ContentRestriction{ID: uuid.NewString(), ContentID: row.registry.ID, Kind: "crisis", SourceRef: caseID, Active: true, Version: 1},
		&entity.CrisisDelivery{ID: uuid.NewString(), CaseID: caseID, Kind: "care", Status: "pending", NextAt: now},
		&entity.CrisisDelivery{ID: uuid.NewString(), CaseID: caseID, Kind: "operator", Status: "pending", NextAt: now},
	} {
		if e = db.Create(v).Error; e != nil {
			return "", e
		}
	}
	// 清除所有公开投影；已送达消息序号保留，由当前危机限制屏蔽正文。
	switch row.registry.Kind {
	case "wish":
		if e = db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", row.wish.ID, row.registry.ContentVersion).Update("public_body", nil).Error; e != nil {
			return "", e
		}
		updates := map[string]any{"version": gorm.Expr("version+1"), "restriction_epoch": gorm.Expr("restriction_epoch+1")}
		if row.wish.State == "published" || row.wish.State == "ready" {
			updates["state"] = "withdrawn"
		}
		e = db.Model(&entity.Wish{}).Where("id=?", row.wish.ID).Updates(updates).Error
	case "story":
		e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", row.wish.ID, row.registry.ContentVersion).Update("public_body", nil).Error
	case "application":
		e = db.Model(&entity.Application{}).Where("id=?", row.registry.EntityID).Update("public_message_cipher", nil).Error
	}
	return caseID, e
}

func crisisRow(db *gorm.DB, id string) (c entity.EvidenceCrisisCase, err error) {
	err = db.Where("id=? AND content_type IN ('wish','application','message','story')", id).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}
func evidenceItem(db *gorm.DB, caseID string) (v entity.EvidenceItem, err error) {
	err = db.Where("case_id=?", caseID).Take(&v).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}
func crisisAction(ctx context.Context, db *gorm.DB, codec *securedata.Codec, c entity.EvidenceCrisisCase, actor, action, reason string, now time.Time) error {
	id := uuid.NewString()
	cipher, _, e := encryptEvidence(ctx, codec, "crisis_reason", id, reason)
	if e != nil {
		return e
	}
	return db.Create(&entity.EvidenceAction{ID: id, CaseID: c.EvidenceCaseID, Actor: actor, Action: action, At: now, ReasonCipher: cipher}).Error
}
