package whisper

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ReadPublicationPost 复用公开投影，为已授权的后台事务读取不含内部身份引用的素材。
func (s *Service) ReadPublicationPost(ctx context.Context, db *gorm.DB, now time.Time, id string) (PublicPost, error) {
	catalog, err := s.resources(ctx)
	if err != nil {
		return PublicPost{}, err
	}
	return s.publicPostBase(ctx, db, now, id, audience{}, catalog)
}

// ReadPublicationComments 仅返回当前帖子版本的可公开评论，按发布时间及 ID 排序。
func (s *Service) ReadPublicationComments(ctx context.Context, db *gorm.DB, now time.Time, id string, offset, limit int) (items []PublicComment, more bool, err error) {
	catalog, err := s.resources(ctx)
	if err != nil {
		return nil, false, err
	}
	parent, err := loadPost(ctx, db, id)
	if err != nil {
		return nil, false, err
	}
	q := visibleCommentsQuery(ctx, db, id, parent.post.CurrentContentVersion, now)
	var ids []string
	if err = q.Order("c.published_at,c.id").Offset(offset).Limit(limit+1).Pluck("c.id", &ids).Error; err != nil {
		return nil, false, err
	}
	more = len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	items = make([]PublicComment, 0, len(ids))
	for _, commentID := range ids {
		item, e := s.publicComment(ctx, db, now, commentID, parent, audience{}, catalog)
		if errors.Is(e, command.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, false, e
		}
		items = append(items, item)
	}
	return items, more, nil
}
