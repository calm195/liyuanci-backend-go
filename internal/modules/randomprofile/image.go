package randomprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/imageinput"
)

type avatarUpload struct {
	Name            string `json:"name,omitempty"`
	ExpectedVersion int64  `json:"expected_version,omitempty"`
	Digest          string `json:"digest"`
}

// CreateAvatar 创建一项通过安全扫描后即启用的素材。
func (s *Service) CreateAvatar(ctx context.Context, token, key, rid, name, mime string, src io.Reader) (Avatar, error) {
	name = strings.TrimSpace(name)
	if !validLabel(name, 80) {
		return Avatar{}, command.ErrInvalid
	}
	return s.upload(ctx, token, key, rid, "", avatarUpload{Name: name}, mime, src)
}

// ReplaceAvatarImage 仅在新图通过扫描后替换当前引用，先前的图片地址继续可用。
func (s *Service) ReplaceAvatarImage(ctx context.Context, token, key, rid, id string, expected int64, mime string, src io.Reader) (Avatar, error) {
	if !canonicalID(id) || expected < 1 {
		return Avatar{}, command.ErrInvalid
	}
	return s.upload(ctx, token, key, rid, id, avatarUpload{ExpectedVersion: expected}, mime, src)
}

func (s *Service) upload(ctx context.Context, token, key, rid, id string, input avatarUpload, mime string, src io.Reader) (_ Avatar, err error) {
	start := time.Now()
	defer func() { report(s, "avatar_scan", rid, start, err) }()
	if key == "" {
		return Avatar{}, command.ErrInvalid
	}
	if err := s.adminRead(ctx, token, nil); err != nil {
		return Avatar{}, err
	}
	data, validation, err := imageinput.Normalize(src, mime, MaxAvatarBytes, maxAvatarPixels)
	defer clear(data)
	if err != nil || validation.Width <= 20 || validation.Height <= 20 {
		return Avatar{}, command.ErrInvalid
	}
	sum := sha256.Sum256(data)
	input.Digest = hex.EncodeToString(sum[:])

	// 图片审核在事务外执行。即使本次扫描故障，命令仍可回放已提交的同键结果。
	display, scanErr := s.scan(ctx, data)
	defer clear(display)
	operation, path := "avatar.create", collectionPath+"/avatars"
	if id != "" {
		operation, path = "avatar.image", path+"/"+id+"/image"
	}
	return write[Avatar](ctx, s, token, key, rid, operation, "POST", path, input, func(tx *command.Tx) (mutation, error) {
		db := tx.DB()
		var before any
		var row entity.RandomProfileAvatar
		if id == "" {
			row = entity.RandomProfileAvatar{ID: uuid.NewString(), Name: input.Name, Enabled: true, Version: 1}
		} else {
			var e error
			row, e = findAvatar(db, id, input.ExpectedVersion)
			if e != nil {
				return mutation{}, e
			}
			before = avatarView(row)
			row.Version++
		}
		if scanErr != nil {
			return mutation{}, scanErr
		}
		image := entity.RandomProfileImage{ID: uuid.NewString(), PNG: display}
		if err := db.Create(&image).Error; err != nil {
			return mutation{}, err
		}
		row.ImageID, row.UpdatedAt = image.ID, tx.Now()
		if err := db.Save(&row).Error; err != nil {
			return mutation{}, err
		}
		return mutation{target: row.ID, before: before, after: avatarView(row)}, nil
	})
}

func (s *Service) scan(ctx context.Context, normalized []byte) ([]byte, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return nil, ErrImageBusy
	}
	display, media, err := imageinput.Display(normalized, 256, true)
	if err != nil || media != "image/png" {
		return nil, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	passed, err := s.scanner.Scan(ctx, normalized, "image/png")
	if errors.Is(err, command.ErrInvalid) || (err == nil && !passed) {
		clear(display)
		return nil, ErrImageRejected
	}
	if err != nil {
		clear(display)
		return nil, command.ErrUnavailable
	}
	return display, nil
}
