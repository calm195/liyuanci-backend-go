package verification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

func (s *Service) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(fn)
}

// Current 只返回本人的最小审核状态，不回显完整身份材料。
func (s *Service) Current(ctx context.Context, token, rid string) (out Result, err error) {
	defer func() {
		if err != nil {
			out = Result{}
		}
	}()
	defer s.done("current", rid, time.Now(), &err)
	err = s.transaction(ctx, func(db *gorm.DB) error {
		id, e := s.authorize(db.Statement.Context, db, token, "student", command.Discovery{})
		if e != nil {
			return e
		}
		r, e := latest(db, id)
		if e != nil {
			return e
		}
		if !slices.Contains([]string{"pending", "approved", "rejected"}, r.Status) {
			return command.ErrNotFound
		}
		out = projection(r)
		return nil
	})
	if err != nil {
		out = Result{}
	}
	return
}

// List 提供无敏感字段的审核队列；游标和页大小只作用于服务器过滤后的可见案件。
func (s *Service) List(ctx context.Context, token, rid, cursor string, limit int) (out Page, err error) {
	defer func() {
		if err != nil {
			out = Page{}
		}
	}()
	defer s.done("list", rid, time.Now(), &err)
	if limit < 1 || limit > 50 || len(cursor) > 256 {
		return out, command.ErrInvalid
	}
	out.Items = []Summary{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorize(db.Statement.Context, db, token, "admin", command.Discovery{})
		if e != nil {
			return e
		}
		after, e := s.decodeCursor(actor, cursor)
		if e != nil {
			return e
		}
		rows, e := listReviews(db, after, limit)
		if e != nil {
			return e
		}
		if len(rows) > limit {
			rows = rows[:limit]
			next := s.encodeCursor(actor, rows[len(rows)-1].ID)
			out.NextCursor = &next
		}
		for _, r := range rows {
			out.Items = append(out.Items, Summary{ID: r.ID, Version: r.Version, Status: r.Status, Kind: "identity_review"})
		}
		return nil
	})
	if err != nil {
		out = Page{}
	}
	return
}

// Detail 在完成数据库访问留迹之后返回解密材料；审计写入失败时不向调用者返回明文。
func (s *Service) Detail(ctx context.Context, token, rid, id string) (out Case, err error) {
	defer func() {
		if err != nil {
			out = Case{}
		}
	}()
	defer s.done("detail", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}

	if e := s.transaction(ctx, func(db *gorm.DB) error {
		_, e := s.authorize(db.Statement.Context, db, token, "admin", command.Discovery{})
		return e
	}); e != nil {
		return out, e
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, r, e := s.lockedCase(db.Statement.Context, db, token, id)
		if e != nil {
			return e
		}
		m, e := s.decrypt(db.Statement.Context, r)
		if e != nil {
			return e
		}
		if e = audit(db, actor, r.AccountID, "identity_review.detail", "allowed"); e != nil {
			return e
		}
		out = Case{ID: r.ID, Version: r.Version, Status: r.Status, Name: m.Name, StudentNumber: m.StudentNumber, AssetID: r.AssetID}
		return nil
	})
	if err != nil {
		out = Case{}
	}
	return
}

// Card 在锁外读取并验证整幅密文，再锁后复核案件版本、生命周期及权限，审计提交后才返回原卡。
// 返回内存由HTTP调用方写出后清零，不使用明文临时文件或可复用下载链接。
func (s *Service) Card(ctx context.Context, token, rid, id string) (out []byte, err error) {
	defer func() {
		if err != nil {
			clear(out)
			out = nil
		}
	}()
	defer s.done("card", rid, time.Now(), &err)
	if !validID(id) {
		return nil, command.ErrInvalid
	}

	if e := s.transaction(ctx, func(db *gorm.DB) error {
		_, e := s.authorize(db.Statement.Context, db, token, "admin", command.Discovery{})
		return e
	}); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	var original entity.IdentityReview
	var stored entity.Asset
	err = s.transaction(ctx, func(db *gorm.DB) error {
		_, r, e := s.lockedCase(db.Statement.Context, db, token, id)
		if e != nil {
			return e
		}
		original = r
		stored, e = asset(db, r.AssetID, r.AccountID)
		return e
	})
	if err != nil {
		return nil, err
	}
	var keyEnv securedata.Envelope
	if json.Unmarshal(stored.EncryptedObjectKey, &keyEnv) != nil {
		return nil, command.ErrUnavailable
	}
	key, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "student_card_key", ObjectID: stored.ID, Version: 1}, keyEnv)
	if e != nil {
		return nil, e
	}
	defer clear(key)
	body, e := s.objects.Open(ctx, string(key), stored.ObjectVersion)
	if e != nil {
		return nil, e
	}
	if body == nil {
		return nil, command.ErrUnavailable
	}
	defer body.Close()
	// JSON信封存在base64膨胀，读取仍使用显式上限，供应商不能通过无界返回耗尽内存。
	capBytes := s.cfg.MaxCardBytes*2 + 65536
	encoded, e := io.ReadAll(io.LimitReader(body, capBytes+1))
	if e != nil || int64(len(encoded)) > capBytes {
		return nil, command.ErrUnavailable
	}
	var env securedata.Envelope
	if json.Unmarshal(encoded, &env) != nil {
		return nil, command.ErrUnavailable
	}
	var image bytes.Buffer
	if e = s.codec.DecryptObject(ctx, securedata.Binding{Purpose: "student_card", ObjectID: stored.ID, Version: 1}, env, &image, s.cfg.MaxCardBytes); e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			clear(image.Bytes())
			out = nil
		}
	}()
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, r, e := s.lockedCase(db.Statement.Context, db, token, id)
		if e != nil {
			return e
		}
		if r.Version != original.Version || r.AssetID != stored.ID {
			return command.ErrVersionConflict
		}
		a, e := asset(db, r.AssetID, r.AccountID)
		if e != nil {
			return e
		}
		if a.Version != stored.Version || a.ObjectVersion != stored.ObjectVersion {
			return command.ErrVersionConflict
		}
		return audit(db, actor, r.AccountID, "identity_review.card", "allowed")
	})
	if err != nil {
		return nil, err
	}
	return image.Bytes(), nil
}

// 游标签名绑定当前后台主体及固定pending队列，不能跨账号或篡改边界。
func (s *Service) cursorMAC(actor, id string) string {
	mac := hmac.New(sha256.New, s.cfg.ReceiptKey)
	mac.Write([]byte("identity_review_queue\x00" + actor + "\x00" + id))
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Service) encodeCursor(actor, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id + "." + s.cursorMAC(actor, id)))
}
func (s *Service) decodeCursor(actor, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, e := base64.RawURLEncoding.DecodeString(cursor)
	if e != nil {
		return "", command.ErrInvalid
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 2 || !validID(parts[0]) || !hmac.Equal([]byte(parts[1]), []byte(s.cursorMAC(actor, parts[0]))) {
		return "", command.ErrInvalid
	}
	return parts[0], nil
}
