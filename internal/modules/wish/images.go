package wish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/imageinput"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// ImageMaxBytes 限制原始输入及去除元数据后的图片大小。
const ImageMaxBytes = 4 << 20

// ImageObjects 提供不可变的私有密文写入与完整性读取。
type ImageObjects interface {
	PutOnce(context.Context, string, []byte) (string, error)
	Open(context.Context, string, string) (io.ReadCloser, error)
}

// ImageScanner 对规范化图片执行安全审核，故障与拒绝分别处理。
type ImageScanner interface {
	Scan(context.Context, []byte, string) (bool, error)
}

// AvatarReader 只为已经允许公开的头像生成短效地址。
type AvatarReader interface {
	AvatarURL(context.Context, string) (string, error)
}

// Image 是客户端可读取的图片引用，不包含私有对象键。
type Image struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Position  int16  `json:"position"`
	ScanState string `json:"scan_state"`
}

// SetImages 由应用装配图片依赖，不执行外部调用。
func (s *Service) SetImages(objects ImageObjects, scanner ImageScanner, avatars AvatarReader) {
	s.imageObjects = objects
	s.imageScanner = scanner
	s.avatarReader = avatars
}

func imageCommand(kind string) command.Definition {
	return command.Definition{Kind: kind, Method: "POST", ResourcePath: "/api/v1/wish-images/" + kind, ActorKind: "student", Access: command.Verified, RetainReceipt: true,
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(context.Context, *command.Tx) error { return nil },
	}
}

// UploadImage 先持久预约稳定密文，再在事务外扫描与写对象，最后复核作者资格完成上传。
// 响应丢失后相同键与相同规范化图片复用原资源，任何阶段失败均保留可清理引用。
func (s *Service) UploadImage(ctx context.Context, token, key, rid, media string, source io.Reader) (out Image, err error) {
	start := time.Now()
	defer func() { s.policy.finish("心愿图片上传完成", "wish.image.upload", rid, start, err) }()
	if !validCommandKey(key) {
		return out, command.ErrInvalid
	}
	if s.imageObjects == nil || s.imageScanner == nil {
		return out, command.ErrUnavailable
	}
	var actor string
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		var e error
		actor, e = currentReader(ctx, db, token, now)
		if e != nil {
			return e
		}
		var account entity.Account
		if e = db.Select("verification").Where("id=?", actor).Take(&account).Error; e != nil {
			return e
		}
		if account.Verification != "verified" {
			return command.ErrForbidden
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	select {
	case s.imageSlots <- struct{}{}:
		defer func() { <-s.imageSlots }()
	default:
		return out, command.ErrUnavailable
	}
	data, validation, err := imageinput.Normalize(source, media, ImageMaxBytes, 16<<20)
	defer clear(data)
	if err != nil || validation.Width <= 20 || validation.Height <= 20 {
		return out, command.ErrInvalid
	}
	sum := sha256.Sum256(data)
	input := command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: hex.EncodeToString(sum[:])}
	var row entity.WishImage
	definition := imageCommand("wish.image.reserve")
	definition.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		id := uuid.NewString()
		owner := tx.Actor().ID
		wrapped, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "wish_image", ObjectID: id, Version: 1}, data)
		if e != nil {
			return "", command.ErrUnavailable
		}
		cipher, e := json.Marshal(wrapped)
		if e != nil {
			return "", command.ErrInternal
		}
		row = entity.WishImage{ID: id, OwnerID: &owner, ObjectKey: "wish-images/" + id, ByteSize: int64(len(data)), Width: validation.Width, Height: validation.Height, ScanState: "pending", UploadCipher: cipher, RemovableAfter: tx.Now().Add(24 * time.Hour)}
		return id, tx.DB().WithContext(ctx).Create(&row).Error
	}
	definition.Resolve = func(ctx context.Context, tx *command.Tx, id string) error {
		if e := tx.DB().WithContext(ctx).Where("id=? AND owner_id=?", id, tx.Actor().ID).Take(&row).Error; e != nil {
			return command.ErrNotFound
		}
		if row.RemovedAt != nil || row.CleanupStartedAt != nil || row.ScanState == "rejected" {
			return command.ErrConflict
		}
		if row.InstalledAt != nil {
			return nil
		}
		if row.UploadUntil != nil && row.UploadUntil.After(tx.Now()) {
			return command.ErrConflict
		}
		until := tx.Now().Add(90 * time.Second)
		changed := tx.DB().Model(&entity.WishImage{}).Where("id=? AND cleanup_started_at IS NULL", id).Update("upload_until", until)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return command.ErrConflict
		}
		row.UploadUntil = &until
		return nil
	}
	if _, err = s.engine.Execute(ctx, definition, input); err != nil {
		return out, err
	}
	if row.InstalledAt != nil {
		return imageProjection(row.ID, "", 0), nil
	}
	defer func() {
		bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.Command.Timeout)
		defer cancel()
		_ = s.db.WithContext(bounded).Model(&entity.WishImage{}).Where("id=? AND upload_until=?", row.ID, *row.UploadUntil).Update("upload_until", nil).Error
	}()
	bounded, cancel := context.WithDeadline(ctx, *row.UploadUntil)
	defer cancel()
	approved, e := s.imageScanner.Scan(bounded, data, "image/png")
	if e != nil {
		return out, command.ErrUnavailable
	}
	if !approved {
		_ = s.db.WithContext(bounded).Model(&entity.WishImage{}).Where("id=? AND upload_until=?", row.ID, *row.UploadUntil).Updates(map[string]any{"scan_state": "rejected", "upload_cipher": nil, "removable_after": time.Now().UTC()}).Error
		return out, command.ErrInvalid
	}
	version, e := s.imageObjects.PutOnce(bounded, row.ObjectKey, row.UploadCipher)
	if e != nil || version == "" {
		return out, command.ErrUnavailable
	}
	install := imageCommand("wish.image.install")
	install.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		changed := tx.DB().WithContext(ctx).Model(&entity.WishImage{}).Where("id=? AND owner_id=? AND upload_until=? AND upload_until>? AND cleanup_started_at IS NULL AND removed_at IS NULL", row.ID, tx.Actor().ID, *row.UploadUntil, tx.Now()).Updates(map[string]any{"object_version": version, "scan_state": "approved", "upload_cipher": nil, "installed_at": tx.Now(), "upload_until": nil})
		if changed.Error != nil {
			return "", changed.Error
		}
		if changed.RowsAffected != 1 {
			return "", command.ErrConflict
		}
		return row.ID, nil
	}
	install.Resolve = func(ctx context.Context, tx *command.Tx, id string) error {
		var current entity.WishImage
		if e := tx.DB().WithContext(ctx).Where("id=? AND owner_id=? AND installed_at IS NOT NULL AND removed_at IS NULL AND cleanup_started_at IS NULL", id, tx.Actor().ID).Take(&current).Error; e != nil {
			return command.ErrNotFound
		}
		out = imageProjection(id, "", 0)
		return nil
	}
	_, err = s.engine.Execute(bounded, install, input)
	return out, err
}

func imageProjection(id, wishID string, position int16) Image {
	url := "/api/v1/wish-images/" + id
	if wishID != "" {
		url += "?wish_id=" + wishID
	}
	return Image{ID: id, URL: url, Position: position, ScanState: "approved"}
}

func bindWishImages(ctx context.Context, db *gorm.DB, id string, version int64, owner string, images []string) error {
	if len(images) > 3 {
		return command.ErrInvalid
	}
	ordered := slices.Clone(images)
	slices.Sort(ordered)
	var rows []entity.WishImage
	if len(ordered) > 0 {
		if e := db.WithContext(ctx).Where("id IN ? AND owner_id=? AND installed_at IS NOT NULL AND scan_state='approved' AND cleanup_started_at IS NULL AND removed_at IS NULL", ordered, owner).Order("id").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) != len(images) {
			return command.ErrInvalid
		}
		blocked, e := imageReferencesBlocked(ctx, db, ordered)
		if e != nil {
			return e
		}
		if blocked {
			return command.ErrInvalid
		}
	}
	for position, image := range images {
		if e := db.WithContext(ctx).Create(&entity.WishVersionImage{WishID: id, ContentVersion: version, ImageID: image, Position: int16(position)}).Error; e != nil {
			return e
		}
	}
	return nil
}

func validateBoundImages(ctx context.Context, db *gorm.DB, id string, version int64) error {
	var invalid int64
	e := db.WithContext(ctx).Table("public.wish_version_images r").Joins("JOIN public.wish_images i ON i.id=r.image_id").Where("r.wish_id=? AND r.content_version=? AND (i.installed_at IS NULL OR i.scan_state<>'approved' OR i.cleanup_started_at IS NOT NULL OR i.removed_at IS NOT NULL)", id, version).Count(&invalid).Error
	if e != nil {
		return e
	}
	if invalid > 0 {
		return command.ErrConflict
	}
	return nil
}

// ReadVersionImages 返回精确内容版本的安全图片引用，调用者须先复核该版本读取资格。
func ReadVersionImages(ctx context.Context, db *gorm.DB, id string, version int64) ([]Image, error) {
	var refs []entity.WishVersionImage
	if e := db.WithContext(ctx).Where("wish_id=? AND content_version=?", id, version).Order("position").Find(&refs).Error; e != nil {
		return nil, e
	}
	out := make([]Image, 0, len(refs))
	for _, ref := range refs {
		out = append(out, imageProjection(ref.ImageID, id, ref.Position))
	}
	return out, nil
}

func (s *Service) authorizeImage(ctx context.Context, token, id, wishID string) (row entity.WishImage, err error) {
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor := ""
		var e error
		if token != "" {
			actor, e = currentReader(ctx, db, token, now)
			if e != nil {
				return e
			}
		}
		if e = db.Where("id=? AND installed_at IS NOT NULL AND scan_state='approved' AND removed_at IS NULL AND cleanup_started_at IS NULL", id).Take(&row).Error; e != nil {
			return command.ErrNotFound
		}
		if wishID == "" {
			if row.OwnerID == nil || *row.OwnerID != actor {
				return command.ErrNotFound
			}
			return authorizeImagePreview(ctx, db, id)
		}
		a, e := readAggregate(ctx, db, wishID, false)
		if e != nil {
			return e
		}
		var bound int64
		if e = db.Model(&entity.WishVersionImage{}).Where("wish_id=? AND content_version=? AND image_id=?", wishID, a.row.CurrentContentVersion, id).Count(&bound).Error; e != nil {
			return e
		}
		if bound != 1 {
			return command.ErrNotFound
		}
		if a.snapshot.PrivacyHeld || a.snapshot.Restricted || !a.owner.Active {
			return command.ErrNotFound
		}
		blocked, e := imageReferencesBlocked(ctx, db, []string{id})
		if e != nil {
			return e
		}
		if blocked {
			return command.ErrNotFound
		}
		if actor == a.row.OwnerID {
			return nil
		}
		if _, e = s.publicContent(ctx, "", now, a); e == nil {
			return nil
		}
		if actor != "" && slices.Contains([]State{Matching, Realized, Fulfilled}, a.snapshot.State) && reviewReady(a.snapshot) {
			var matched int64
			if e = db.Model(&entity.Match{}).Where("wish_id=? AND helper_id=? AND status='current'", wishID, actor).Count(&matched).Error; e != nil {
				return e
			}
			if matched == 1 {
				return nil
			}
		}
		return command.ErrNotFound
	})
	return
}

// authorizeImagePreview 不让上传时的无来源URL绕过后来的删除、换图或治理屏障。
// 尚未绑定的上传可供作者预览；一旦绑定，只允许仍属于当前未删除正文的图片。
func authorizeImagePreview(ctx context.Context, db *gorm.DB, id string) error {
	var scope struct {
		Referenced, Current int64
	}
	err := db.WithContext(ctx).Raw(`SELECT
 (SELECT count(*) FROM public.wish_version_images WHERE image_id=?) AS referenced,
 (SELECT count(*) FROM public.wish_version_images r JOIN public.wishes w ON w.id=r.wish_id
  WHERE r.image_id=? AND r.content_version=w.current_content_version AND w.draft_deleted_at IS NULL) AS current`, id, id).Scan(&scope).Error
	if err != nil {
		return err
	}
	blocked, err := imageReferencesBlocked(ctx, db, []string{id})
	if err != nil {
		return err
	}
	if blocked || scope.Referenced > 0 && scope.Current == 0 {
		return command.ErrNotFound
	}
	return nil
}

// imageReferencesBlocked 检查全部引用来源，不能通过复制到另一个心愿绕过原来源屏障。
func imageReferencesBlocked(ctx context.Context, db *gorm.DB, images []string) (bool, error) {
	var blocked int64
	err := db.WithContext(ctx).Raw(`SELECT
 (SELECT count(*) FROM public.privacy_holds h WHERE h.target_kind='wish' AND h.status='active'
  AND h.target_id IN(SELECT wish_id FROM public.wish_version_images WHERE image_id IN ?))+
 (SELECT count(*) FROM public.content_restrictions x JOIN public.content_registry c ON c.id=x.content_id
  WHERE x.active AND c.kind='wish' AND c.entity_id IN(SELECT wish_id FROM public.wish_version_images WHERE image_id IN ?))`, images, images).Scan(&blocked).Error
	return blocked > 0, err
}

// ReadImage 在外部读取前后复核权限，撤下或屏障不能通过旧URL继续读取。
func (s *Service) ReadImage(ctx context.Context, token, rid, id, wishID string) (data []byte, err error) {
	return s.ReadImageVariant(ctx, token, rid, id, wishID, "")
}

// ReadImageVariant 的展示图和原图共享完整权限边界，缓存不能绕过下架、换图或治理。
func (s *Service) ReadImageVariant(ctx context.Context, token, rid, id, wishID, variant string) (data []byte, err error) {
	defer s.queryDone("wish.image.read", rid, time.Now(), &err)
	if !validWishID(id) || wishID != "" && !validWishID(wishID) || !validImageVariant(variant) {
		return nil, command.ErrInvalid
	}
	row, err := s.authorizeImage(ctx, token, id, wishID)
	if err != nil {
		return nil, err
	}
	data, err = s.readImageVariant(ctx, row, variant)
	if err != nil {
		return nil, err
	}
	if _, err = s.authorizeImage(ctx, token, id, wishID); err != nil {
		clear(data)
		return nil, err
	}
	return
}

// ReadReviewedImage 仅供已经复核案件和精确图片归属的后台装配入口读取，不注册为无授权路由。
func (s *Service) ReadReviewedImage(ctx context.Context, id string) ([]byte, error) {
	return s.ReadReviewedImageVariant(ctx, id, "")
}

// ReadReviewedImageVariant 由后台装配层在读取前后复核当前案件权限。
func (s *Service) ReadReviewedImageVariant(ctx context.Context, id, variant string) ([]byte, error) {
	if !validImageVariant(variant) {
		return nil, command.ErrInvalid
	}
	var row entity.WishImage
	if e := s.db.WithContext(ctx).Where("id=? AND installed_at IS NOT NULL AND removed_at IS NULL", id).Take(&row).Error; e != nil {
		return nil, command.ErrNotFound
	}
	return s.readImageVariant(ctx, row, variant)
}

func (s *Service) readImageObject(ctx context.Context, row entity.WishImage) ([]byte, error) {
	if s.imageObjects == nil {
		return nil, command.ErrUnavailable
	}
	raw, e := s.imageReads.load(ctx, imageCacheKey(row, "original"), func() ([]byte, error) {
		reader, err := s.imageObjects.Open(ctx, row.ObjectKey, row.ObjectVersion)
		if err != nil {
			return nil, command.ErrUnavailable
		}
		defer reader.Close()
		data, err := io.ReadAll(io.LimitReader(reader, ImageMaxBytes*2+1))
		if err != nil || len(data) > ImageMaxBytes*2 {
			clear(data)
			return nil, command.ErrUnavailable
		}
		return data, nil
	})
	defer clear(raw)
	if e != nil || len(raw) > ImageMaxBytes*2 {
		return nil, command.ErrUnavailable
	}
	var envelope securedata.Envelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, command.ErrUnavailable
	}
	data, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "wish_image", ObjectID: row.ID, Version: 1}, envelope)
	if e != nil || len(data) == 0 || len(data) > ImageMaxBytes {
		clear(data)
		return nil, command.ErrUnavailable
	}
	return data, nil
}
