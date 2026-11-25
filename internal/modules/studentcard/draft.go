package studentcard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// Draft 是本人可继续使用的照片及当前正在更换的照片。
type Draft struct {
	Active    *Asset `json:"active"`
	Candidate *Asset `json:"candidate"`
}

func assetView(row entity.Asset) Asset {
	return Asset{AssetID: row.ID, ScanStatus: row.ScanStatus, ScannedAt: row.ScannedAt}
}

func draftSlot(db *gorm.DB, owner, slot string) *gorm.DB {
	return db.Model(&entity.Asset{}).Where("owner_id=? AND purpose='student_card' AND student_card_slot=?", owner, slot)
}

func loadDraft(db *gorm.DB, owner string) (out Draft, err error) {
	var review entity.IdentityReview
	err = db.Select("asset_id", "status").Where("account_id=?", owner).Order("version DESC").Take(&review).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return out, err
	}
	if review.Status == "approved" || review.Status == "pending" {
		return out, nil
	}
	var rows []entity.Asset
	err = db.Where(`owner_id=? AND purpose='student_card'
		AND status NOT IN ('deleting','deleted')
		AND (student_card_slot IN ('current','candidate')
			OR (id::text=? AND student_card_slot IS NULL AND status='active' AND scan_status='approved'))`, owner, review.AssetID).Find(&rows).Error
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		view := assetView(row)
		if row.StudentCardSlot == nil {
			if out.Active == nil {
				out.Active = &view
			}
		} else if *row.StudentCardSlot == "current" && row.ScanStatus == "approved" {
			out.Active = &view
		} else if *row.StudentCardSlot == "candidate" {
			out.Candidate = &view
		}
	}
	return out, nil
}

func (s *Service) withDraft(ctx context.Context, token string, fn func(*gorm.DB, string, Draft) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		owner, err := authorizeStudent(ctx, db, token, s.config.Command.LockTimeout)
		if err != nil {
			return err
		}
		draft, err := loadDraft(db, owner)
		if err != nil {
			return err
		}
		return fn(db, owner, draft)
	})
}

// Draft 通过本人会话查找仍可继续办理的材料。
func (s *Service) Draft(ctx context.Context, token, requestID string) (out Draft, err error) {
	start := time.Now()
	defer func() { s.complete("draft", requestID, start, err) }()
	err = safe(s.withDraft(ctx, token, func(_ *gorm.DB, _ string, draft Draft) error {
		out = draft
		return nil
	}))
	return out, err
}

// RemoveDraft 只移除调用方已读取的照片，迟到请求不会清除新上传的候选。
func (s *Service) RemoveDraft(ctx context.Context, token, id, requestID string) (err error) {
	start := time.Now()
	defer func() { s.complete("remove_draft", requestID, start, err) }()
	if !validID(id) {
		return command.ErrInvalid
	}
	return safe(s.withDraft(ctx, token, func(db *gorm.DB, owner string, draft Draft) error {
		if (draft.Active == nil || draft.Active.AssetID != id) && (draft.Candidate == nil || draft.Candidate.AssetID != id) {
			return command.ErrNotFound
		}
		return removeDraftAssets(db.Model(&entity.Asset{}).Where("id=? AND owner_id=?", id, owner))
	}))
}

// 草稿槽位与清理时间同时写入，同一账号的上传、提交和清理用同一锁序。
func removeDraftAssets(query *gorm.DB) error {
	return query.Updates(map[string]any{
		"student_card_slot":       "removed",
		"student_card_cleanup_at": gorm.Expr("clock_timestamp()"),
	}).Error
}

type objectReader interface {
	Open(context.Context, string, string) (io.ReadCloser, error)
}

// Preview 读取当前本人草稿中的已扫描图片，外部读图结束后再次复核归属。
func (s *Service) Preview(ctx context.Context, token, id, requestID string) (out []byte, err error) {
	start := time.Now()
	defer func() {
		err = safe(err)
		s.complete("preview", requestID, start, err)
	}()
	if !validID(id) {
		return nil, command.ErrInvalid
	}
	reader, ok := s.objects.(objectReader)
	if !ok {
		return nil, command.ErrUnavailable
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return nil, ErrBusy
	}
	var original entity.Asset
	check := func(db *gorm.DB, _ string, draft Draft) error {
		if draft.Active == nil || draft.Active.AssetID != id {
			return command.ErrNotFound
		}
		row, e := readAsset(db.Statement.Context, db, id)
		if e != nil {
			return e
		}
		if original.ID != "" && (original.Version != row.Version || original.ObjectVersion != row.ObjectVersion) {
			return command.ErrNotFound
		}
		original = row
		return nil
	}
	if err = s.withDraft(ctx, token, check); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	var keyEnvelope securedata.Envelope
	if json.Unmarshal(original.EncryptedObjectKey, &keyEnvelope) != nil {
		return nil, command.ErrUnavailable
	}
	key, err := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "student_card_key", ObjectID: id, Version: 1}, keyEnvelope)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	body, err := reader.Open(ctx, string(key), original.ObjectVersion)
	if err != nil || body == nil {
		return nil, command.ErrUnavailable
	}
	defer body.Close()
	limit := s.config.MaxBytes*2 + 65536
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, command.ErrUnavailable
	}
	var envelope securedata.Envelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, command.ErrUnavailable
	}
	var image bytes.Buffer
	if err = s.codec.DecryptObject(ctx, securedata.Binding{Purpose: "student_card", ObjectID: id, Version: 1}, envelope, &image, s.config.MaxBytes); err != nil {
		clear(image.Bytes())
		return nil, err
	}
	if err = s.withDraft(ctx, token, check); err != nil {
		clear(image.Bytes())
		return nil, err
	}
	return image.Bytes(), nil
}
