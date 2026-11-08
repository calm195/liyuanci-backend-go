package wish

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

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
		if *a.snapshot.Review.SchoolRelated {
			original.NoticeText = "未经证实"
		}
		return original, &p, nil
	}
	original, err = s.publicContent(requestID, now, a)
	if err != nil && !errors.Is(err, command.ErrNotFound) && !errors.Is(err, command.ErrInternal) {
		err = command.ErrNotFound
	}
	return original, nil, err
}
