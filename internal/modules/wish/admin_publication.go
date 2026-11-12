package wish

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ReadPublicationChain 为已授权的后台事务读取可用于公众号的已审投影。
// 完成的心愿只有在当前还愿故事公开时，才能连同原始已审心愿正文组成发布素材。
func (s *Service) ReadPublicationChain(ctx context.Context, db *gorm.DB, now time.Time, requestID, id string) (original PublicContent, story *PublicContent, err error) {
	a, err := readAggregate(ctx, db, id, false)
	if err != nil {
		return original, nil, err
	}
	if a.row.State == string(Fulfilled) {
		p, e := s.publicStory(ctx, db, id)
		if e != nil {
			return original, nil, e
		}
		if a.row.PublishedAt == nil || !reviewReady(a.snapshot) {
			return original, nil, command.ErrNotFound
		}
		original.Title = publicTitle(a.content)
		original.Body = a.snapshot.Review.PublicBody
		original.ID = a.row.ID
		original.Images = a.images
		original.Author = s.publicAuthor(ctx, a, a.snapshot.Review.Tags)
		original.IdentityMode = a.row.IdentityMode
		if *a.snapshot.Review.SchoolRelated {
			original.NoticeText = "未经证实"
		}
		return original, &p, nil
	}
	original, err = s.publicContent(ctx, requestID, now, a)
	if err != nil && !errors.Is(err, command.ErrNotFound) && !errors.Is(err, command.ErrInternal) {
		err = command.ErrNotFound
	}
	return original, nil, err
}

// ReadAdminMetadata 供已授权的后台事务读取非正文统计与期限，不赋予内容或图片读取权限。
func (s *Service) ReadAdminMetadata(ctx context.Context, db *gorm.DB, now time.Time, id string) (PublicContent, error) {
	var row entity.Wish
	if err := db.WithContext(ctx).Where("id=? AND draft_deleted_at IS NULL", id).Take(&row).Error; err != nil {
		return PublicContent{}, err
	}
	items := []PublicContent{{ID: row.ID, FirstPublishedAt: row.FirstPublishedAt, PublishedAt: row.PublishedAt, ExpiresAt: row.ExpiresAt}}
	if err := s.enrichWishMetadata(ctx, db, items, "", now); err != nil {
		return PublicContent{}, err
	}
	return items[0], nil
}
