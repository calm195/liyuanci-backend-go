package matchmaking

import (
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// resultState 使用服务端时刻和实际发布事实分流；公布计划时间不能证明运营已经发布。
func resultState(activity entity.MatchmakingActivity, enrollment *Enrollment, pair *Pair, published bool, now time.Time) string {
	if activity.Status == "cancelled" || !now.Before(activity.EndsAt) || pair != nil && pair.Status == "ended" {
		return "ended"
	}
	if enrollment == nil || enrollment.Status == "withdrawn" || enrollment.Status == "rejected" {
		return "not_enrolled"
	}
	if enrollment.Status == "pending" {
		return "pending_review"
	}
	if pair != nil && pair.Status == "published" {
		return "paired"
	}
	if published {
		return "unmatched"
	}
	return "waiting"
}
