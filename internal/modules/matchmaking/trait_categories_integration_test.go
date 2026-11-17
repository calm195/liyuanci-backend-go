package matchmaking_test

import (
	"fmt"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
)

// TestPostgresHTTPTraitCategories 验证共享分类不能被同名建词或学生保存覆盖，停用与审核独立。
func TestPostgresHTTPTraitCategories(t *testing.T) {
	f := setup(t)
	a, b := f.account("verified"), f.account("verified")
	input := matchmaking.TraitCreateInput{Name: "真诚", Category: "personality", Reason: "合成词条"}
	f.call("POST", "/admin/matchmaking/traits", a, "student-session", input, 401)
	f.call("POST", "/admin/matchmaking/traits", f.account("crisis_operator"), "forbidden", input, 403)
	created := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/traits", f.admin, "create-category", input, 200))
	f.call("POST", "/admin/matchmaking/traits", f.admin, "create-category", input, 200)
	input.Category = "interest"
	f.call("POST", "/admin/matchmaking/traits", f.admin, "same-key-other-content", input, 409)
	f.call("POST", "/admin/matchmaking/traits", f.admin, "create-category", input, 409)
	path := "/admin/matchmaking/traits/" + created.ID
	get := func() matchmaking.TraitDetail {
		return decoded[matchmaking.TraitDetail](t, f.call("GET", path, f.admin, "", nil, 200))
	}
	if got := get(); got.Category != "personality" || !got.Enabled || got.Version != 1 {
		t.Fatalf("词条被覆盖：%+v", got)
	}
	save := func(who actor, names []string, categories map[string]string, status int) {
		current := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", who, "", nil, 200))
		p := profile("合成", "male").Preferences
		p.PersonalTraits = names
		p.PersonalTraitCategories = categories
		f.call("PUT", "/matchmaking/profile", who, who.id+"-"+string(rune(current.Version+65)), matchmaking.ProfileInput{ExpectedVersion: current.Version, Profile: p}, status)
	}
	save(a, []string{"真诚"}, map[string]string{"真诚": "interest"}, 200)
	if got := get(); got.Category != "personality" {
		t.Fatal("学生覆盖了共享分类")
	}
	suggestions := decoded[matchmaking.TraitCloud](t, f.call("GET", "/matchmaking/traits?purpose=personal&category=personality&q=", a, "", nil, 200))
	if len(suggestions.Items) != 1 || suggestions.Items[0].Category != "personality" {
		t.Fatal("空关键词分类推荐错误")
	}
	before := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	enabled := false
	update := matchmaking.TraitUpdateInput{ExpectedVersion: 1, Category: "lifestyle", Enabled: &enabled, Reason: "合成停用"}
	f.call("PUT", path, f.admin, "update-category", update, 200)
	f.call("PUT", path, f.admin, "update-category", update, 200)
	f.call("PUT", path, f.admin, "update-stale", update, 409)
	if got := get(); got.Status != "approved" || got.Reason != "合成词条" || got.Enabled || got.Category != "lifestyle" || got.Version != 2 {
		t.Fatalf("审核/分类耦合或幂等失效：%+v", got)
	}
	after := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	if after.AccountVersion <= before.AccountVersion || after.Profile.PersonalTraitCategories["真诚"] != "lifestyle" {
		t.Fatal("旧报名预览或服务端分类未刷新")
	}
	save(a, []string{"真诚"}, nil, 200)
	save(b, []string{"真诚"}, nil, 409)
	suggestions = decoded[matchmaking.TraitCloud](t, f.call("GET", "/matchmaking/traits?q=", a, "", nil, 200))
	if len(suggestions.Items) != 0 {
		t.Fatal("停用词仍被推荐")
	}
	f.call("GET", "/matchmaking/traits?category=other", a, "", nil, 400)
	// 停用关系不能通过换用途绕过；用户仍可明确移除旧关系。
	p := profile("合成", "male").Preferences
	p.IdealTraits = []string{"真诚"}
	current := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	f.call("PUT", "/matchmaking/profile", a, "disabled-other-purpose", matchmaking.ProfileInput{ExpectedVersion: current.Version, Profile: p}, 409)
	save(a, []string{}, nil, 200)
	if got := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200)); len(got.TraitSelections) != 0 {
		t.Fatal("停用历史关系无法移除")
	}
	// 每种用途各10，而非总共10；合法英文词条可以作为分类映射名称。
	p = profile("合成", "male").Preferences
	p.PersonalTraitCategories = map[string]string{"ENFP": "personality"}
	p.PersonalTraits = []string{"ENFP"}
	for i := 1; i < 10; i++ {
		p.PersonalTraits = append(p.PersonalTraits, fmt.Sprintf("个人%d", i))
	}
	for i := 0; i < 10; i++ {
		p.IdealTraits = append(p.IdealTraits, fmt.Sprintf("理想%d", i))
	}
	current = decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", a, "", nil, 200))
	f.call("PUT", "/matchmaking/profile", a, "twenty-independent", matchmaking.ProfileInput{ExpectedVersion: current.Version, Profile: p}, 200)
	p.PersonalTraits = append(p.PersonalTraits, "第十一个")
	f.call("PUT", "/matchmaking/profile", a, "over-purpose-limit", matchmaking.ProfileInput{ExpectedVersion: current.Version + 1, Profile: p}, 400)
	for i, body := range []map[string]any{
		{"expected_version": 2, "category": "other", "enabled": true, "reason": "合成"},
		{"expected_version": 2, "category": "interest", "enabled": nil, "reason": "合成"},
		{"expected_version": 2, "category": "interest", "enabled": true, "reason": ""},
		{"expected_version": 2, "category": "interest", "enabled": true, "reason": "合成\x00理由"},
		{"expected_version": 2, "category": "interest", "enabled": true, "reason": "合成", "status": "approved"},
	} {
		f.call("PUT", path, f.admin, fmt.Sprintf("invalid-fields-%d", i), body, 400)
	}
	// 管理维护审计只随成功命令提交；原键重放和版本失败不重复追加。
	var audits []entity.MatchmakingTraitMaintenanceAudit
	must(t, f.db.Where("trait_id=?", created.ID).Order("version").Find(&audits).Error)
	if len(audits) != 2 || audits[0].Action != "create" || audits[0].OldCategory != nil || audits[0].OldEnabled != nil || audits[0].Reason != "合成词条" || audits[1].Action != "category_enabled" || audits[1].ActorID != f.admin.id || audits[1].Version != 2 || audits[1].Reason != "合成停用" || audits[1].OldCategory == nil || *audits[1].OldCategory != "personality" || audits[1].NewCategory != "lifestyle" || audits[1].OldEnabled == nil || !*audits[1].OldEnabled || audits[1].NewEnabled || audits[1].At.IsZero() {
		t.Fatalf("维护审计不完整或重放重复：%+v", audits)
	}
	update = matchmaking.TraitUpdateInput{ExpectedVersion: 2, Category: "interest", Enabled: &enabled, Reason: "仅变更分类"}
	f.call("PUT", path, f.admin, "audit-category", update, 200)
	enabled = true
	update.ExpectedVersion = 3
	update.Reason = "仅启用"
	f.call("PUT", path, f.admin, "audit-enable", update, 200)
	update.ExpectedVersion = 4
	update.Reason = "确认现有配置"
	f.call("PUT", path, f.admin, "audit-confirm", update, 200)
	must(t, f.db.Exec("ALTER TABLE public.matchmaking_trait_maintenance_audits ADD CONSTRAINT synthetic_audit_reject CHECK(reason<>'合成拒绝审计')").Error)
	enabled = false
	update.ExpectedVersion = 5
	update.Reason = "合成拒绝审计"
	f.call("PUT", path, f.admin, "audit-atomic", update, 409)
	must(t, f.db.Exec("ALTER TABLE public.matchmaking_trait_maintenance_audits DROP CONSTRAINT synthetic_audit_reject").Error)
	if got := get(); got.Version != 5 || !got.Enabled || got.Reason != "合成词条" {
		t.Fatal("审计失败未回滚维护或破坏原审核理由")
	}
	must(t, f.db.Where("trait_id=?", created.ID).Order("version").Find(&audits).Error)
	if len(audits) != 5 || audits[2].Action != "category" || audits[3].Action != "enabled" || audits[4].Action != "confirm" {
		t.Fatal("审计动作或原子性错误")
	}
}
