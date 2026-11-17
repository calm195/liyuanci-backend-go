package matchmaking_test

import (
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
)

// TestPostgresHTTPResultPublication 区分计划时间、真实发布事实与结束后的资料访问边界。
func TestPostgresHTTPResultPublication(t *testing.T) {
	f := setup(t)
	a, b, c := f.account("verified"), f.account("verified"), f.account("verified")
	id := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "result-activity", activityInput(), 200)).ID
	f.command(id, "publish", 200)
	e1 := f.enroll(id, a, profile("甲", "male"), 0)
	e2 := f.enroll(id, b, profile("乙", "female"), 0)
	e3 := f.enroll(id, c, profile("丙", "male"), 0)
	for _, eid := range []string{e1, e2, e3} {
		f.review(id, eid, 1)
	}
	now := time.Now().UTC()
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).Updates(map[string]any{"registration_closes_at": now.Add(-time.Hour), "results_at": now.Add(-30 * time.Minute), "starts_at": now.Add(-time.Minute)}).Error)
	if got := f.detail(id, c).ResultState; got != "waiting" {
		t.Fatal("计划时间被误认为实际发布", got)
	}
	f.call("POST", adminPath(id)+"/pairs", f.admin, "result-pair", matchmaking.PairInput{ExpectedVersion: f.workspace(id).Activity.Version, First: e1, Second: e2}, 200)
	if got := f.detail(id, a); got.ResultState != "waiting" || got.Pair != nil {
		t.Fatal("草稿配对提前泄露")
	}
	f.template(id, []matchmaking.Template{{Kind: "online", Body: "一", FixedDay: 1}, {Kind: "online", Body: "二", FixedDay: 2}, {Kind: "online", Body: "三", FixedDay: 3}}, 200)
	f.command(id, "results/publish", 200)
	paired := f.detail(id, a)
	if paired.ResultState != "paired" || paired.Pair == nil || paired.Pair.Peer == nil || !paired.Pair.Accessible {
		t.Fatal("已发布配对不可读取")
	}
	if got := f.detail(id, c).ResultState; got != "unmatched" {
		t.Fatal("实际公布后未配对错误", got)
	}
	// 历史发布审计仍然是可信发布事实，个人任务删除不抹除活动结果。
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).UpdateColumn("tasks_published_at", nil).Error)
	if got := f.detail(id, c).ResultState; got != "unmatched" {
		t.Fatal("历史发布事实丢失", got)
	}
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).UpdateColumn("ends_at", now.Add(-time.Second)).Error)
	ended := f.detail(id, a)
	if ended.ResultState != "ended" || ended.Pair == nil || ended.Pair.Accessible || ended.Pair.Peer != nil || ended.Pair.ConversationID != "" || len(ended.Tasks) != 0 {
		t.Fatal("结束后仍泄露有效配对入口")
	}
}
