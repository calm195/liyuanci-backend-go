package resourceplaza

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// FileMaxBytes 是资料文件上传的统一大小上限。
const FileMaxBytes = 20 << 20

// FileObjects 提供资料独立命名空间内的不可覆盖写入与短效下载。
type FileObjects interface {
	PutResource(context.Context, string, []byte, string) (string, error)
	ResourceURL(context.Context, string, string) (string, error)
}

// FileInfo 只公开文件展示字段，不返回对象键或上传者。
type FileInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

func fileInfo(row entity.ResourceFile) *FileInfo {
	return &FileInfo{ID: row.ID, Name: row.Name, ContentType: row.ContentType, Size: row.ByteSize}
}

func validateFile(name string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > FileMaxBytes || !utf8.ValidString(name) || len([]rune(name)) > 200 || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return "", command.ErrInvalid
	}
	detected := http.DetectContentType(data)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		if !strings.HasPrefix(string(data[:min(len(data), 5)]), "%PDF-") {
			return "", command.ErrInvalid
		}
		return "application/pdf", nil
	case ".txt", ".csv":
		if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return "", command.ErrInvalid
		}
		return "text/plain; charset=utf-8", nil
	case ".png", ".jpg", ".jpeg":
		if detected != "image/png" && detected != "image/jpeg" {
			return "", command.ErrInvalid
		}
		return detected, nil
	case ".zip", ".docx", ".xlsx", ".pptx":
		if len(data) < 4 || string(data[:2]) != "PK" {
			return "", command.ErrInvalid
		}
		return map[string]string{".zip": "application/zip", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation"}[strings.ToLower(filepath.Ext(name))], nil
	case ".doc", ".xls", ".ppt":
		if len(data) < 8 || string(data[:8]) != "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1" {
			return "", command.ErrInvalid
		}
		return "application/octet-stream", nil
	}
	return "", command.ErrInvalid
}

// UploadFile 先持久化上传预约，再写私有对象；失败记录可重试并由清理进程回收。
func (s *Service) UploadFile(ctx context.Context, token, key, rid, name string, source io.Reader) (out *FileInfo, err error) {
	defer s.done("file_upload", rid, time.Now(), &err)
	if s.cfg.Objects == nil {
		return nil, command.ErrUnavailable
	}
	if key == "" || len(key) > 128 || strings.ContainsFunc(key, func(r rune) bool { return r < 33 || r > 126 }) {
		return nil, command.ErrInvalid
	}
	// 在读取大文件和调用外部存储前先验证当前管理员权限。
	if err = s.transaction(ctx, token, true, false, func(*gorm.DB, actor) error { return nil }); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(source, FileMaxBytes+1))
	if err != nil {
		return nil, command.ErrInvalid
	}
	defer clear(data)
	media, err := validateFile(name, data)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	var row entity.ResourceFile
	var leased bool
	err = s.transaction(ctx, token, true, true, func(db *gorm.DB, a actor) error {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("resource-file\x00"+a.ID+"\x00"+key)).String()
		e := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			row = entity.ResourceFile{ID: id, OwnerID: a.ID, ObjectKey: "resources/" + id, Name: name, ContentType: media, ByteSize: int64(len(data)), SHA256: hash, CreatedAt: a.Now, RemovableAfter: a.Now.Add(24 * time.Hour)}
		} else if e != nil {
			return e
		}
		if row.SHA256 != hash || row.Name != name || row.ContentType != media {
			return command.ErrIdempotencyConflict
		}
		if row.CleanupStartedAt != nil || row.RemovedAt != nil {
			return command.ErrConflict
		}
		if row.Ready {
			return nil
		}
		if row.UploadUntil != nil && row.UploadUntil.After(a.Now) {
			return command.ErrConflict
		}
		until := a.Now.Add(2 * time.Minute)
		row.UploadUntil = &until
		leased = true
		return db.Save(&row).Error
	})
	if err != nil {
		return nil, err
	}
	if !leased {
		return fileInfo(row), nil
	}
	bounded, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	version, putErr := s.cfg.Objects.PutResource(bounded, row.ObjectKey, data, media)
	if putErr != nil || version != "sha256:"+hash {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.Command.Timeout)
		defer cancel()
		_ = s.db.WithContext(release).Model(&entity.ResourceFile{}).Where("id=? AND upload_until=?", row.ID, row.UploadUntil).Update("upload_until", nil).Error
		return nil, command.ErrUnavailable
	}
	err = s.transaction(ctx, token, true, true, func(db *gorm.DB, a actor) error {
		result := db.Model(&entity.ResourceFile{}).Where("id=? AND owner_id=? AND upload_until=? AND upload_until>? AND cleanup_started_at IS NULL", row.ID, a.ID, row.UploadUntil, a.Now).Updates(map[string]any{"ready": true, "upload_until": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return command.ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fileInfo(row), nil
}

// bindFile 仅允许绑定本人新上传文件或资料当前文件，锁住文件防止与清理并发。
func bindFile(db *gorm.DB, a actor, id string, current *string) (*string, error) {
	if id == "" {
		return nil, nil
	}
	var row entity.ResourceFile
	if e := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND ready AND cleanup_started_at IS NULL AND removed_at IS NULL", id).Take(&row).Error; e != nil {
		return nil, command.ErrConflict
	}
	if row.OwnerID != a.ID && (current == nil || *current != id) {
		return nil, command.ErrForbidden
	}
	return &row.ID, nil
}

func (s *Service) fileDownload(db *gorm.DB, r entity.ResourceMaterial) (*FileInfo, string, error) {
	if r.FileID == nil {
		return nil, "", nil
	}
	if s.cfg.Objects == nil {
		return nil, "", command.ErrUnavailable
	}
	var row entity.ResourceFile
	if e := db.Where("id=? AND ready AND cleanup_started_at IS NULL AND removed_at IS NULL", *r.FileID).Take(&row).Error; e != nil {
		return nil, "", command.ErrUnavailable
	}
	link, e := s.cfg.Objects.ResourceURL(db.Statement.Context, row.ObjectKey, row.Name)
	return fileInfo(row), link, e
}
