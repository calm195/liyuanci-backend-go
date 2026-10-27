package wish

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func readProfile(ctx context.Context, db *gorm.DB, target string) (out ProfileSummary, err error) {
	err = db.WithContext(ctx).Model(&entity.Account{}).Select("id,nickname").Where("id=? AND lifecycle='active'", target).Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

type profileCandidate struct {
	ID, Kind string
	Position time.Time
}

// profileCandidates 只取排序与类型，公开资格由现有单内容读取再次复核。
// 心愿和故事只可能有一种当前公开状态，因而公开时间与父心愿ID足以唯一定位。
func profileCandidates(ctx context.Context, db *gorm.DB, target string, after pageCursor, now time.Time, stories bool) (rows []profileCandidate, err error) {
	position := "CASE WHEN w.state='published' THEN w.published_at ELSE v.published_at END"
	q := db.WithContext(ctx).Table("public.wishes AS w").
		Select("w.id, CASE WHEN w.state='published' THEN 'wish' ELSE 'story' END AS kind, "+position+" AS position").
		Joins("LEFT JOIN public.story_versions v ON v.wish_id=w.id AND v.story_version=w.current_story_version").
		Where("w.owner_id=? AND w.draft_deleted_at IS NULL", target).
		Where("(w.state='published' AND w.expires_at>? AND w.published_at IS NOT NULL) OR (? AND w.state='fulfilled' AND v.published_at IS NOT NULL)", now, stories)
	if after.ID != "" {
		q = q.Where("("+position+",w.id)<(?,?::uuid)", after.PublishedAt, after.ID)
	}
	err = q.Order(position + " DESC,w.id DESC").Limit(100).Scan(&rows).Error
	return
}
