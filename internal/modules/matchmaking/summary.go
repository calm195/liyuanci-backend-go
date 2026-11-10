package matchmaking

import "gorm.io/gorm"

// loadEndedSummaries 在列表或详情的读取快照内按活动汇总，整页最多查询一次。
func loadEndedSummaries(db *gorm.DB, activities []Activity) error {
	ids := make([]string, 0, len(activities))
	for _, activity := range activities {
		if activity.Phase == "ended" {
			ids = append(ids, activity.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var rows []struct {
		ID string
		ActivitySummary
	}
	err := db.Raw(`
SELECT a.id, COALESCE(e.total, 0) AS enrollment_count, COALESCE(p.total, 0) AS successful_pair_count
FROM public.matchmaking_activities a
LEFT JOIN (
    SELECT activity_id, count(*) AS total
    FROM public.matchmaking_enrollments
    WHERE activity_id IN ? AND status <> 'withdrawn'
    GROUP BY activity_id
) e ON e.activity_id = a.id
LEFT JOIN (
    SELECT activity_id, count(*) AS total
    FROM public.matchmaking_pairs
    WHERE activity_id IN ? AND status = 'published'
    GROUP BY activity_id
) p ON p.activity_id = a.id
WHERE a.id IN ?`, ids, ids, ids).Scan(&rows).Error
	if err != nil {
		return err
	}
	summaries := make(map[string]*ActivitySummary, len(rows))
	for _, row := range rows {
		summary := row.ActivitySummary
		summaries[row.ID] = &summary
	}
	for i := range activities {
		activities[i].Summary = summaries[activities[i].ID]
	}
	return nil
}
