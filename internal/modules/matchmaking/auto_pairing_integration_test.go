package matchmaking_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// TestPostgresHTTPAutoPairing 验证持久预览、重启补偿、过期输入、占用保留与原子幂等确认。
func TestPostgresHTTPAutoPairing(t *testing.T) {
	f := setup(t)
	codec, err := securedata.New(syntheticKeys{}, zap.NewNop())
	must(t, err)
	worker, err := matchmaking.NewAutoWorker(f.db, zap.NewNop(), codec, durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3})
	must(t, err)
	activity := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "auto-activity", activityInput(), 200)).ID
	f.command(activity, "publish", 200)
	enrollments := []string{}
	actors := []actor{}
	for i := 0; i < 6; i++ {
		a := f.account("verified")
		actors = append(actors, a)
		gender := "male"
		if i%2 == 1 {
			gender = "female"
		}
		p := profile(fmt.Sprintf("合成自动%d", i), gender)
		p.PreferredCampuses = []string{"wushan"}
		if gender == "male" {
			p.PreferredCampuses = []string{"international"}
		}
		p.PreferredMBTI = []string{"INFP"}
		p.PersonalTraits = []string{"温柔"}
		p.IdealTraits = []string{"温柔"}
		enrollment := f.enroll(activity, a, p, 0)
		enrollments = append(enrollments, enrollment)
		f.review(activity, enrollment, 1)
	}
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", activity).Update("registration_closes_at", time.Now().Add(-time.Minute)).Error)
	// 既有手动草稿先占用两人，自动分配不得覆盖。
	version := f.workspace(activity).Activity.Version
	existing := decoded[matchmaking.Receipt](t, f.call("POST", adminPath(activity)+"/pairs", f.admin, "existing-draft", matchmaking.PairInput{ExpectedVersion: version, First: enrollments[0], Second: enrollments[1]}, 200)).ID
	prefix := adminPath(activity) + "/auto-pairing-jobs"
	generate := func(key string) string {
		t.Helper()
		v := f.workspace(activity).Activity.Version
		return decoded[matchmaking.Receipt](t, f.call("POST", prefix, f.admin, key, matchmaking.VersionInput{ExpectedVersion: v}, 200)).ID
	}
	read := func(id string) matchmaking.AutoJob {
		return decoded[matchmaking.AutoJob](t, f.call("GET", prefix+"/"+id, f.admin, "", nil, 200))
	}
	job := generate("generate-first")
	f.call("POST", prefix, f.admin, "duplicate-running", matchmaking.VersionInput{ExpectedVersion: f.workspace(activity).Activity.Version}, 409)
	if read(job).Status != "pending" {
		t.Fatal("未持久化待处理任务")
	}
	// 模拟进程退出时已标记running，重建worker仍应使用原seed完成。
	must(t, f.db.Model(&entity.MatchmakingAutoJob{}).Where("id=?", job).Update("status", "running").Error)
	outcome, e := worker.ProcessNext(context.Background())
	must(t, e)
	if !outcome.Applied {
		t.Fatal("未消费持久任务")
	}
	ready := read(job)
	if ready.Status != "ready" || ready.PairedCount != 2 || ready.EligibleCount != 4 || ready.ExcludedCount != 2 || len(ready.Pairs) != 2 {
		t.Fatalf("方案统计错误：%+v", ready)
	}
	for _, p := range ready.Pairs {
		if p.ScoreDetails.Traits != 10 || p.ScoreDetails.PreferredCampus != 2.5 || p.ScoreDetails.PreferredMBTI != 10 || p.Score != float64(int(p.Score))+0.5 {
			t.Fatal("自动计分未使用双向标签")
		}
	}
	if latest := read("latest"); latest.ID != job {
		t.Fatal("最新任务错误")
	}
	f.call("GET", prefix+"/"+job, actors[0], "", nil, 403)
	var count int64
	must(t, f.db.Model(&entity.MatchmakingPair{}).Where("activity_id=?", activity).Count(&count).Error)
	if count != 1 {
		t.Fatal("预览阶段占用了报名者")
	}
	// 即使活动版本未变化，新增拉黑也必须让预览失效。
	must(t, f.db.Create(&entity.Block{OwnerID: actors[2].id, TargetID: actors[3].id}).Error)
	f.call("POST", prefix+"/"+job+"/apply", f.admin, "stale-input", matchmaking.AutoApplyInput{ExpectedVersion: ready.Version, ActivityVersion: ready.ActivityVersion}, 409)
	if read(job).Status != "stale" {
		t.Fatal("输入变化未使任务失效")
	}
	job = generate("generate-second")
	_, e = worker.ProcessNext(context.Background())
	must(t, e)
	ready = read(job)
	if ready.Status != "ready" || ready.PairedCount != 2 {
		t.Fatal("拉黑约束下应重新找到两组")
	}
	apply := matchmaking.AutoApplyInput{ExpectedVersion: ready.Version, ActivityVersion: ready.ActivityVersion}
	f.call("POST", prefix+"/"+job+"/apply", f.admin, "apply-once", apply, 200)
	f.call("POST", prefix+"/"+job+"/apply", f.admin, "apply-once", apply, 200)
	must(t, f.db.Model(&entity.MatchmakingPair{}).Where("activity_id=? AND status='draft'", activity).Count(&count).Error)
	if count != 3 {
		t.Fatalf("确认重复或漏写：%d", count)
	}
	var old entity.MatchmakingPair
	must(t, f.db.Where("id=?", existing).Take(&old).Error)
	if old.Version != 1 {
		t.Fatal("已占用草稿被改写")
	}
	if read(job).Status != "applied" {
		t.Fatal("确认任务未完成")
	}
	// 通过词条在personal/ideal双用途分别保存，清空其中一种不影响另一种。
	account := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", actors[0], "", nil, 200))
	if len(account.TraitSelections) != 2 || len(account.Profile.PersonalTraits) != 1 {
		t.Fatal("双用途资料未独立保存")
	}
	f.call("PUT", "/matchmaking/profile", actors[0], "clear-personal", map[string]any{"expected_version": account.Version, "profile": map[string]any{"personal_traits": []string{}}}, 200)
	account = decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", actors[0], "", nil, 200))
	if len(account.Profile.PreferredCampuses) != 1 || account.Profile.PreferredCampuses[0] != "international" || len(account.Profile.PreferredMBTI) != 1 {
		t.Fatal("省略偏好未保留原值")
	}
	if len(account.Profile.PersonalTraits) != 0 || len(account.Profile.IdealTraits) != 1 {
		t.Fatal("清空个人特质影响理想型")
	}
	for _, spec := range []struct {
		purpose string
		heat    int64
	}{{"personal", 5}, {"ideal", 6}, {"all", 6}} {
		page := decoded[matchmaking.TraitPage](t, f.call("GET", "/admin/matchmaking/traits?purpose="+spec.purpose, f.admin, "", nil, 200))
		if len(page.Items) != 1 || page.Items[0].Heat != spec.heat {
			t.Fatalf("用途统计错误：%s %+v", spec.purpose, page)
		}
	}
	detail := decoded[matchmaking.TraitDetail](t, f.call("GET", "/admin/matchmaking/traits/"+account.TraitSelections[0].ID, f.admin, "", nil, 200))
	if detail.Heat != 6 {
		t.Fatal("词条详情all热度未按账号去重")
	}
	// 显式空集合恢复不限，不改变既有报名快照和已保存配对。
	f.call("PUT", "/matchmaking/profile", actors[0], "clear-preferences", map[string]any{"expected_version": account.Version, "profile": map[string]any{"preferred_campuses": []string{}, "preferred_mbti": []string{}}}, 200)
	cleared := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", actors[0], "", nil, 200))
	if len(cleared.Profile.PreferredCampuses) != 0 || len(cleared.Profile.PreferredMBTI) != 0 {
		t.Fatal("空集合未清除偏好")
	}
	must(t, f.db.Model(&entity.Account{}).Where("id=?", actors[0].id).Updates(map[string]any{"enrollment_year": 2019, "education_level": "undergraduate", "grade_override": "undergraduate_2"}).Error)
	educated := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", actors[0], "", nil, 200))
	if educated.Profile.Grade != "undergraduate_2" || educated.Profile.GradeLabel != "大二" || educated.Profile.EnrollmentYear != 2019 {
		t.Fatal("共享账号年级投影不一致")
	}
	// 孤儿工作应被持久消费者取消，不能永久阻塞后续任务。
	orphan := generate("orphan-job")
	must(t, f.db.Where("id=?", orphan).Delete(&entity.MatchmakingAutoJob{}).Error)
	outcome, e = worker.ProcessNext(context.Background())
	must(t, e)
	if !outcome.Cancelled {
		t.Fatal("孤儿预览任务未取消")
	}

	// 升级前遗留的空方案也不能绕过算法版本校验。
	legacy := generate("legacy-empty-job")
	must(t, f.db.Model(&entity.MatchmakingAutoJob{}).Where("id=?", legacy).Updates(map[string]any{"algorithm": "legacy-algorithm", "status": "ready"}).Error)
	var legacyRow entity.MatchmakingAutoJob
	must(t, f.db.Where("id=?", legacy).Take(&legacyRow).Error)
	f.call("POST", prefix+"/"+legacy+"/apply", f.admin, "legacy-apply", matchmaking.AutoApplyInput{ExpectedVersion: legacyRow.Version, ActivityVersion: legacyRow.ActivityVersion}, 409)
	if read(legacy).Status != "stale" {
		t.Fatal("旧算法任务未失效")
	}

}
