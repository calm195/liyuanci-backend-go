package contentquery

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Stats 是按目标聚合的浏览数与当前读者私有收藏状态。
type Stats struct {
	ViewCount int64 `json:"view_count"`
	Favorited bool  `json:"favorited"`
}

// ValidInteractionKind 限制跨模块统计入口的固定内容种类。
func ValidInteractionKind(kind string) bool {
	return kind == "wish" || kind == "whisper_post" || kind == "resource"
}

// ReadStats 批量装配当前页统计，不读取收藏者名单或浏览者轨迹。
func ReadStats(ctx context.Context, db *gorm.DB, kind string, ids []string, actor string) (map[string]Stats, error) {
	out := make(map[string]Stats, len(ids))
	if !ValidInteractionKind(kind) {
		return nil, command.ErrInvalid
	}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		TargetID string
		Stats
	}
	err := db.WithContext(ctx).Raw(`SELECT target_id,view_count,
		EXISTS(SELECT 1 FROM public.content_favorites f WHERE f.target_kind=s.target_kind
		AND f.target_id=s.target_id AND f.account_id=NULLIF(?,'')::uuid) AS favorited
		FROM public.content_statistics s WHERE s.target_kind=? AND s.target_id IN ?`, actor, kind, ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.TargetID] = row.Stats
	}
	// 没有浏览事件的目标也可能已收藏，不用统计行存在性推断收藏状态。
	if actor != "" {
		var favorites []string
		if err = db.WithContext(ctx).Table("public.content_favorites").Where("target_kind=? AND target_id IN ? AND account_id=?", kind, ids, actor).Pluck("target_id", &favorites).Error; err != nil {
			return nil, err
		}
		for _, id := range favorites {
			v := out[id]
			v.Favorited = true
			out[id] = v
		}
	}
	return out, nil
}

// RecordView 在调用方已复核当前可见性后原子去重计数；技术事件键不保存用户身份。
// 事件最多保留两日；清理有界，避免读取路径累计无界行为日志。
func RecordView(ctx context.Context, db *gorm.DB, kind, id, eventID string, now time.Time) error {
	if !ValidInteractionKind(kind) {
		return command.ErrInvalid
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var inserted bool
		if err := tx.Raw(`WITH inserted AS (
			INSERT INTO public.content_view_events(event_id,target_kind,target_id,created_at)
			VALUES (?,?,?,?) ON CONFLICT DO NOTHING RETURNING event_id
		) SELECT EXISTS(SELECT 1 FROM inserted)`, eventID, kind, id, now).Scan(&inserted).Error; err != nil {
			return err
		}
		if !inserted {
			return nil
		}

		if err := tx.Exec(`INSERT INTO public.content_statistics(target_kind,target_id,view_count)
			VALUES (?,?,1) ON CONFLICT(target_kind,target_id)
			DO UPDATE SET view_count=content_statistics.view_count+1`, kind, id).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM public.content_view_events WHERE event_id IN
			(SELECT event_id FROM public.content_view_events WHERE created_at<? ORDER BY created_at LIMIT 100)`, now.Add(-48*time.Hour)).Error
	})
}
