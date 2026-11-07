package studentauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/imageinput"
)

// AvatarMaxBytes 限制原图及规范化PNG的字节数。
const AvatarMaxBytes = 2 << 20

const (
	avatarMaxPixels      = 4 << 20
	avatarUploadTimeout  = 25 * time.Second
	avatarReservationTTL = 5 * time.Minute
)

// AvatarObjects 独立提供头像对象写入及短效读取地址。
type AvatarObjects interface {
	PutAvatar(context.Context, string, []byte) (string, error)
	AvatarURL(context.Context, string) (string, error)
}

// AvatarScanner 审核已移除元数据的头像。
type AvatarScanner interface {
	Scan(context.Context, []byte, string) (bool, error)
}

type avatarUpload struct {
	Version int64  `json:"expected_version"`
	Digest  string `json:"digest"`
}

func avatarCommand(kind, method, path string) command.Definition {
	return command.Definition{
		Kind:          kind,
		Method:        method,
		ResourcePath:  path,
		ActorKind:     "student",
		Access:        command.Ordinary,
		RetainReceipt: true,
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(context.Context, *command.Tx) error { return nil },
	}
}

func requireAvatarVersion(tx *command.Tx, expected int64) error {
	guard, ok := tx.Guard(tx.Actor().ID)
	if !ok {
		return command.ErrInternal
	}
	if guard.Version != expected {
		return command.ErrVersionConflict
	}
	return nil
}

func readAvatar(ctx context.Context, tx *command.Tx, id string) (row entity.AccountAvatar, err error) {
	err = tx.DB().WithContext(ctx).Where("id=? AND owner_id=?", id, tx.Actor().ID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

func (s *Service) reserveAvatar(ctx context.Context, input command.Input, upload avatarUpload, size int64) (row entity.AccountAvatar, err error) {
	// 持久预约先于COS上传。即使进程退出，独立清理进程也能找到可能存在的对象。
	definition := avatarCommand("student.avatar.reserve", "POST", "/api/v1/me/avatar/reservation")
	definition.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		if e := requireAvatarVersion(tx, upload.Version); e != nil {
			return "", e
		}
		id := uuid.NewString()
		owner := tx.Actor().ID
		avatar := entity.AccountAvatar{
			ID:             id,
			OwnerID:        &owner,
			ObjectKey:      "avatars/" + id + ".png",
			ObjectVersion:  upload.Digest,
			ByteSize:       size,
			RemovableAfter: tx.Now().Add(avatarReservationTTL),
		}
		return id, tx.DB().WithContext(ctx).Create(&avatar).Error
	}
	definition.Resolve = func(ctx context.Context, tx *command.Tx, id string) error {
		var e error
		row, e = readAvatar(ctx, tx, id)
		if e != nil || row.InstalledAt != nil {
			return e
		}
		if row.CleanupStartedAt != nil || row.RemovedAt != nil ||
			!row.RemovableAfter.After(tx.Now().Add(avatarUploadTimeout)) ||
			row.UploadUntil != nil && row.UploadUntil.After(tx.Now()) {
			return command.ErrConflict
		}
		if e = requireAvatarVersion(tx, upload.Version); e != nil {
			return e
		}
		lease := tx.Now().Add(avatarUploadTimeout)
		updated := tx.DB().WithContext(ctx).Model(&entity.AccountAvatar{}).Where("id=? AND owner_id=? AND cleanup_started_at IS NULL", id, tx.Actor().ID).Update("upload_until", lease)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return command.ErrConflict
		}
		row.UploadUntil = &lease
		return nil
	}
	_, err = s.engine.Execute(ctx, definition, input)
	return
}

// UploadAvatar 规范化并审核头像，随后在资料版本复核通过时原子替换当前头像。
func (s *Service) UploadAvatar(ctx context.Context, token, key, requestID string, expected int64, mediaType string, source io.Reader) (out Me, err error) {
	start := time.Now()
	defer func() {
		err = safe(err)
		s.complete("avatar_upload", requestID, start, err)
	}()
	if s.config.AvatarObjects == nil || s.config.AvatarScanner == nil {
		return out, command.ErrUnavailable
	}
	if !validKey(key) || expected < 1 {
		return out, command.ErrInvalid
	}
	me, err := s.Current(ctx, token, requestID)
	if err != nil {
		return out, err
	}
	if !slices.Contains(me.AllowedActions, "putMeProfile") {
		return out, command.ErrForbidden
	}
	select {
	case s.avatarSlots <- struct{}{}:
		defer func() { <-s.avatarSlots }()
	default:
		return out, ErrRateLimited
	}
	data, _, err := imageinput.Normalize(source, mediaType, AvatarMaxBytes, avatarMaxPixels)
	defer clear(data)
	if err != nil {
		return out, command.ErrInvalid
	}
	digest := sha256.Sum256(data)
	upload := avatarUpload{Version: expected, Digest: "sha256:" + hex.EncodeToString(digest[:])}
	input := command.Input{Token: token, IdempotencyKey: key, RequestID: requestID, Request: upload}
	row, err := s.reserveAvatar(ctx, input, upload, int64(len(data)))
	if err != nil {
		return out, err
	}
	if row.InstalledAt != nil {
		return s.Current(ctx, token, requestID)
	}
	bounded, stop := context.WithDeadline(ctx, *row.UploadUntil)
	defer stop()
	defer s.releaseAvatarLease(ctx, row)
	passed, err := s.config.AvatarScanner.Scan(bounded, data, "image/png")
	if err != nil {
		return out, command.ErrUnavailable
	}
	if !passed {
		return out, command.ErrInvalid
	}
	version, err := s.config.AvatarObjects.PutAvatar(bounded, row.ObjectKey, data)
	if err != nil || version != row.ObjectVersion {
		return out, command.ErrUnavailable
	}
	return s.changeAvatar(bounded, input, expected, &row)
}

// releaseAvatarLease 只释放本次预约，超时后已转移给重试请求的预约保持有效。
func (s *Service) releaseAvatarLease(ctx context.Context, row entity.AccountAvatar) {
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.Command.Timeout)
	defer cancel()
	if err := s.db.WithContext(bounded).Model(&entity.AccountAvatar{}).
		Where("id=? AND upload_until=?", row.ID, *row.UploadUntil).Update("upload_until", nil).Error; err != nil {
		s.logger.Warn("头像上传预约释放失败")
	}
}

func (s *Service) changeAvatar(ctx context.Context, input command.Input, expected int64, candidate *entity.AccountAvatar) (out Me, err error) {
	definition := avatarCommand("student.avatar.install", "POST", "/api/v1/me/avatar")
	if candidate == nil {
		definition = avatarCommand("student.avatar.remove", "DELETE", "/api/v1/me/avatar")
	}
	definition.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		if e := requireAvatarVersion(tx, expected); e != nil {
			return "", e
		}
		db := tx.DB().WithContext(ctx)
		owner := tx.Actor().ID
		var account entity.Account
		if e := db.Select("avatar_id").Where("id=?", owner).Take(&account).Error; e != nil {
			return "", e
		}
		var avatarID *string
		if candidate != nil {
			avatarID = &candidate.ID
			updated := db.Model(&entity.AccountAvatar{}).
				Where("id=? AND owner_id=? AND installed_at IS NULL AND removed_at IS NULL AND cleanup_started_at IS NULL AND upload_until=? AND upload_until>?", *avatarID, owner, *candidate.UploadUntil, tx.Now()).
				Updates(map[string]any{"installed_at": tx.Now(), "upload_until": nil})
			if updated.Error != nil {
				return "", updated.Error
			}
			if updated.RowsAffected != 1 {
				return "", command.ErrConflict
			}
		}
		if account.AvatarID != nil {
			if e := db.Model(&entity.AccountAvatar{}).Where("id=? AND owner_id=?", *account.AvatarID, owner).
				Update("removable_after", tx.Now()).Error; e != nil {
				return "", e
			}
		}
		return owner, db.Model(&entity.Account{}).Where("id=?", owner).
			Updates(map[string]any{"avatar_id": avatarID, "avatar_url": "", "version": gorm.Expr("version+1")}).Error
	}
	definition.Resolve = func(ctx context.Context, tx *command.Tx, id string) error {
		var e error
		out, e = s.project(ctx, tx.DB(), id, tx.Now())
		return e
	}
	_, err = s.engine.Execute(ctx, definition, input)
	if err != nil {
		out = Me{}
	}
	return out, safe(err)
}

// DeleteAvatar 切换回默认头像，旧对象由账号清理进程回收。
func (s *Service) DeleteAvatar(ctx context.Context, token, key, requestID string, expected int64) (Me, error) {
	if !validKey(key) || expected < 1 {
		return Me{}, command.ErrInvalid
	}
	return s.changeAvatar(ctx, command.Input{Token: token, IdempotencyKey: key, RequestID: requestID, Request: expected}, expected, nil)
}
