package matchmaking_test

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
)

// TestPostgresHTTPTraits 覆盖真实数据库与 HTTP 的建词、去重、审核、统计、权限及报名快照。
func TestPostgresHTTPTraits(t *testing.T) {
	f := setup(t)
	a, b := f.account("verified"), f.account("verified")
	must(t, f.db.Model(&entity.Account{}).Where("id=?", a.id).Update("identity_tags", json.RawMessage(`{"gender":"male","campus":"wushan","grade":"undergraduate_3"}`)).Error)
	must(t, f.db.Model(&entity.Account{}).Where("id=?", b.id).Update("identity_tags", json.RawMessage(`{"gender":"female","campus":"wushan","grade":"undergraduate_3"}`)).Error)
	save := func(who actor, names []string) {
		current := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", who, "", nil, 200))
		p := profile("合成", "male").Preferences
		p.IdealTraits = names
		f.call("PUT", "/matchmaking/profile", who, "save-"+who.id+"-"+string(rune(current.Version+65)), matchmaking.ProfileInput{ExpectedVersion: current.Version, Profile: p}, 200)
	}
	save(a, []string{" 温柔 ", "温柔", "待审爱好"})
	save(b, []string{"温柔"})
	current := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	if len(current.TraitSelections) != 2 || len(current.Profile.IdealTraits) != 1 || current.Profile.IdealTraits[0] != "温柔" {
		t.Fatalf("本人状态投影错误：%+v", current)
	}
	pending := ""
	for _, row := range current.TraitSelections {
		if row.Status == "pending" {
			pending = row.ID
		}
	}
	if pending == "" {
		t.Fatal("缺少待审词条")
	}
	suggestions := decoded[matchmaking.TraitCloud](t, f.call("GET", "/matchmaking/traits?q="+url.QueryEscape("待审"), b, "", nil, 200))
	if len(suggestions.Items) != 0 {
		t.Fatal("待审词条泄露到推荐")
	}
	list := decoded[matchmaking.TraitPage](t, f.call("GET", "/admin/matchmaking/traits", f.admin, "", nil, 200))
	if len(list.Items) != 1 || list.Items[0].Heat != 2 || list.Items[0].MaleCount != 1 || list.Items[0].FemaleCount != 1 {
		t.Fatalf("去重统计错误：%+v", list)
	}
	cloud := decoded[matchmaking.TraitCloud](t, f.call("GET", "/admin/matchmaking/traits/cloud?gender=female&min_heat=1&max_heat=1", f.admin, "", nil, 200))
	if cloud.Total != 1 || len(cloud.Items) != 1 || cloud.Items[0].Heat != 1 {
		t.Fatalf("词云筛选错误：%+v", cloud)
	}
	f.call("GET", "/admin/matchmaking/traits", a, "", nil, 403)
	f.call("GET", "/admin/matchmaking/traits/"+pending, a, "", nil, 403)
	f.call("GET", "/matchmaking/traits?status=pending", a, "", nil, 400)
	f.call("GET", "/admin/matchmaking/traits?gender=male&gender=female", f.admin, "", nil, 400)
	f.call("GET", "/admin/matchmaking/traits?min_heat=3&max_heat=1", f.admin, "", nil, 400)
	detail := decoded[matchmaking.TraitDetail](t, f.call("GET", "/admin/matchmaking/traits/"+pending, f.admin, "", nil, 200))
	if detail.Status != "pending" || len(detail.Evidence) == 0 {
		t.Fatal("审核案件缺少依据")
	}
	review := matchmaking.ReviewInput{ExpectedVersion: 1, Decision: "approved", Reason: "合成审核通过"}
	f.call("POST", "/admin/matchmaking/traits/"+pending+"/review", f.admin, "approve-trait", review, 200)
	f.call("POST", "/admin/matchmaking/traits/"+pending+"/review", f.admin, "approve-trait", review, 200)
	f.call("POST", "/admin/matchmaking/traits/"+pending+"/review", f.admin, "stale-trait", review, 409)
	approved := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	if len(approved.Profile.IdealTraits) != 2 || approved.AccountVersion == current.AccountVersion {
		t.Fatal("审核未生效或旧报名预览未失效")
	}
	// 老客户端省略字段保留关系；显式空数组清空。
	save(a, nil)
	retained := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	if len(retained.TraitSelections) != 2 {
		t.Fatal("省略标签误清空")
	}
	save(a, []string{})
	list = decoded[matchmaking.TraitPage](t, f.call("GET", "/admin/matchmaking/traits?q="+url.QueryEscape("温柔"), f.admin, "", nil, 200))
	if list.Items[0].Heat != 1 {
		t.Fatal("移除未减少热度")
	}
	must(t, f.db.Model(&entity.Account{}).Where("id=?", b.id).Update("identity_tags", json.RawMessage(`{"gender":"undisclosed"}`)).Error)
	cloud = decoded[matchmaking.TraitCloud](t, f.call("GET", "/admin/matchmaking/traits/cloud?gender=undisclosed", f.admin, "", nil, 200))
	if cloud.Total != 1 || cloud.Items[0].Heat != 1 {
		t.Fatal("性别变更统计未更新")
	}
	save(a, []string{"待审拒绝"})
	rejected := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200)).TraitSelections[0]
	f.call("POST", "/admin/matchmaking/traits/"+rejected.ID+"/review", f.admin, "reject-empty", matchmaking.ReviewInput{ExpectedVersion: 1, Decision: "rejected"}, 400)
	f.call("POST", "/admin/matchmaking/traits/"+rejected.ID+"/review", f.admin, "reject-trait", matchmaking.ReviewInput{ExpectedVersion: 1, Decision: "rejected", Reason: "合成拒绝原因"}, 200)
	rejectedProfile := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	if len(rejectedProfile.TraitSelections) != 0 || len(rejectedProfile.Profile.IdealTraits) != 0 {
		t.Fatal("驳回投影错误")
	}
	// 同一词条再次选择复用原审核结论，不创建新案件绕过驳回。
	save(b, []string{"待审拒绝"})
	other := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", b, "", nil, 200))
	if len(other.TraitSelections) != 0 {
		t.Fatal("驳回词条被重复建词")
	}
	// 匹配优先于热度，精确词即使无人选用也排在热门包含词之前。
	save(a, []string{"柔", "柔和", "温柔"})
	save(a, []string{"温柔"})
	save(b, []string{"温柔"})
	ranked := decoded[matchmaking.TraitCloud](t, f.call("GET", "/matchmaking/traits?q="+url.QueryEscape("柔"), a, "", nil, 200))
	if len(ranked.Items) != 3 || ranked.Items[0].Name != "柔" || ranked.Items[1].Name != "柔和" || ranked.Items[2].Name != "温柔" {
		t.Fatalf("匹配排序错误：%+v", ranked)
	}
	// 新报名只截取已通过词条，审核不改写旧快照，旧预览不能悄然添加新词。
	activity := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "traits-activity", activityInput(), 200)).ID
	f.command(activity, "publish", 200)
	p := profile("合成特质甲", "male")
	p.IdealTraits = []string{"温柔", "待审快照"}
	f.enroll(activity, a, p, 0)
	preview := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	for _, selection := range preview.TraitSelections {
		if selection.Status == "pending" {
			f.call("POST", "/admin/matchmaking/traits/"+selection.ID+"/review", f.admin, "approve-snapshot", matchmaking.ReviewInput{ExpectedVersion: 1, Decision: "approved"}, 200)
		}
	}
	if got := f.detail(activity, a).Enrollment.Profile.IdealTraits; len(got) != 1 || got[0] != "温柔" {
		t.Fatalf("历史快照被审核改写：%v", got)
	}
	f.call("PUT", studentPath(activity)+"/enrollment", a, "stale-trait-preview", matchmaking.EnrollmentInput{ExpectedVersion: 1, ProfileVersion: preview.Version, AccountVersion: preview.AccountVersion, Consent: true}, 409)
	// 交叉用途并发创建必须统一词库锁序，并复用唯一词条。
	c, d := f.account("verified"), f.account("verified")
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, who := range []actor{c, d} {
		wg.Add(1)
		go func(who actor) {
			defer wg.Done()
			p := matchmaking.Preferences{IdealTraits: []string{"并发特质"}, PersonalTraits: []string{"跨用途特质"}}
			if who.id == d.id {
				p.IdealTraits, p.PersonalTraits = p.PersonalTraits, p.IdealTraits
			}
			_, err := f.service.SaveProfile(context.Background(), who.token, "concurrent-trait-"+who.id, "synthetic", matchmaking.ProfileInput{Profile: p})
			failures <- err
		}(who)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		must(t, err)
	}
	for _, who := range []actor{c, d} {
		p := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", who, "", nil, 200))
		if len(p.Profile.IdealTraits) != 1 || len(p.Profile.PersonalTraits) != 1 || p.Profile.IdealTraits[0] == p.Profile.PersonalTraits[0] {
			t.Fatal("交叉用途保存丢失或混淆标签")
		}
	}
	concurrent := decoded[matchmaking.TraitPage](t, f.call("GET", "/admin/matchmaking/traits?q="+url.QueryEscape("并发特质"), f.admin, "", nil, 200))
	if concurrent.Total != 1 || concurrent.Items[0].Heat != 2 {
		t.Fatalf("并发建词未去重：%+v", concurrent)
	}

}
