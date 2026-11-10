package matchmaking_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
)

// TestPostgresHTTPTaskLibrary 验证独立题库、活动排程预检及补发布沿用冻结内容。
func TestPostgresHTTPTaskLibrary(t *testing.T) {
	f := setup(t)
	const library = "/admin/matchmaking/task-library"
	const activities = "/admin/matchmaking/activities"
	a, b, c, d := f.account("verified"), f.account("verified"), f.account("verified"), f.account("verified")
	f.call("GET", library, a, "", nil, 403)
	enabled, disabled := true, false
	add := func(body string, day int, enabled *bool) string {
		t.Helper()
		in := matchmaking.LibraryTaskInput{Body: body, Kind: "online", MinDay: day, Enabled: enabled}
		key := uuid.NewString()
		created := decoded[matchmaking.Receipt](t, f.call("POST", library, f.admin, key, in, 200))
		if replay := decoded[matchmaking.Receipt](t, f.call("POST", library, f.admin, key, in, 200)); replay != created {
			t.Fatal("任务库新建幂等回执改变")
		}
		return created.ID
	}
	first := add("合成首日任务", 0, &enabled)
	third := add("合成第三日任务", 3, &enabled)
	add("已停用的合成任务", 2, &disabled)
	page := decoded[matchmaking.LibraryPage](t, f.call("GET", library+"?page=1&limit=2", f.admin, "", nil, 200))
	if page.Total != 3 || len(page.Items) != 2 || page.Items[0].ID != first || page.Items[1].Enabled {
		t.Fatal("任务库独立分页或启用状态错误")
	}
	input := activityInput()
	id := decoded[matchmaking.Receipt](t, f.call("POST", activities, f.admin, uuid.NewString(), input, 200)).ID
	plan := matchmaking.TaskPlanInput{ExpectedVersion: f.workspace(id).Activity.Version, FirstTaskID: third}
	f.call("PUT", adminPath(id)+"/task-plan", f.admin, uuid.NewString(), plan, 409)
	plan.FirstTaskID = first
	f.call("PUT", adminPath(id)+"/task-plan", f.admin, uuid.NewString(), plan, 200)
	// 保存活动草稿仍保留单独设置的首日任务。
	input.ExpectedVersion = f.workspace(id).Activity.Version
	f.call("PUT", adminPath(id), f.admin, uuid.NewString(), input, 200)
	if current := f.workspace(id).TaskPlan; current == nil || current.FirstTaskID != first || current.MissingDay != 2 {
		t.Fatalf("活动任务预检未识别第二日缺口：%+v", current)
	}

	f.command(id, "publish", 200)
	enrollments := []string{
		f.enroll(id, a, profile("合成库测试甲", "male"), 0),
		f.enroll(id, b, profile("合成库测试乙", "female"), 0),
		f.enroll(id, c, profile("合成库测试丙", "male"), 0),
		f.enroll(id, d, profile("合成库测试丁", "female"), 0),
	}
	for _, enrollment := range enrollments {
		f.review(id, enrollment, 1)
	}
	now := time.Now().UTC().Truncate(time.Second)
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).Updates(map[string]any{
		"registration_closes_at": now.Add(-30 * time.Minute), "results_at": now.Add(-20 * time.Minute),
	}).Error)
	pair := func(i int) string {
		t.Helper()
		in := matchmaking.PairInput{ExpectedVersion: f.workspace(id).Activity.Version, First: enrollments[i], Second: enrollments[i+1]}
		return decoded[matchmaking.Receipt](t, f.call("POST", adminPath(id)+"/pairs", f.admin, uuid.NewString(), in, 200)).ID
	}
	firstPair := pair(0)
	f.command(id, "results/publish", 409)
	f.unpublished(id)
	add("合成第二日任务", 2, &enabled)
	if current := f.workspace(id).TaskPlan; current == nil || current.MissingDay != 0 {
		t.Fatalf("题库补齐后仍报告缺口：%+v", current)
	}
	f.command(id, "results/publish", 200)

	update := matchmaking.LibraryTaskInput{ExpectedVersion: 1, Body: "编辑后的首日任务", Kind: "offline", Enabled: &disabled}
	f.call("PUT", library+"/"+first, f.admin, uuid.NewString(), update, 200)
	f.call("PUT", library+"/"+first, f.admin, uuid.NewString(), update, 409)
	current := f.workspace(id)
	if !current.TemplatesLocked || current.TaskPlan == nil || current.TaskPlan.FirstTaskBody != "合成首日任务" {
		t.Fatal("修改独立题库影响已冻结的活动任务")
	}
	f.call("PUT", adminPath(id)+"/task-plan", f.admin, uuid.NewString(),
		matchmaking.TaskPlanInput{ExpectedVersion: current.Activity.Version, FirstTaskID: first}, 409)
	secondPair := pair(2)
	f.command(id, "results/publish", 200)
	for _, pairID := range []string{firstPair, secondPair} {
		var bodies []string
		must(t, f.db.Model(&entity.MatchmakingTask{}).Where("pair_id=?", pairID).Order("day_index").Pluck("body", &bodies).Error)
		if want := []string{"合成首日任务", "合成第二日任务", "合成第三日任务"}; !reflect.DeepEqual(bodies, want) {
			t.Fatalf("首次或补发任务快照错误：%v", bodies)
		}
	}
}
