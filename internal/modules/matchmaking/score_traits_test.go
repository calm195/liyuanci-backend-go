package matchmaking

import (
	"reflect"
	"testing"
)

func TestScoreTraitsBothDirections(t *testing.T) {
	a, b := ruleProfile(), ruleProfile()
	a.MBTI, b.MBTI = "INFP", "ENFJ"
	base, old := Score(a, b)
	if base != 100 || old.Traits != 0 {
		t.Fatalf("旧资料基础分变化: %d %+v", base, old)
	}
	a.PersonalTraits = []string{"温柔", "开朗", "温柔"}
	a.IdealTraits = []string{"自律", "温柔", "温柔"}
	b.PersonalTraits = []string{"自律", "温柔", "自律"}
	b.IdealTraits = []string{"温柔", "开朗", "开朗", "善良"}
	score, d := Score(a, b)
	if score != 120 || d.Traits != 20 || len(d.PersonalToIdeal) != 2 || len(d.IdealToPersonal) != 2 {
		t.Fatalf("双向交集去重计分错误: %d %+v", score, d)
	}
	if d.MBTI != old.MBTI || d.Age != old.Age || d.Campus != old.Campus || d.Height != old.Height || d.Weight != old.Weight {
		t.Fatal("标签改变原有基础项")
	}
	reverse, rd := Score(b, a)
	if reverse != score || !reflect.DeepEqual(d.PersonalToIdeal, rd.IdealToPersonal) || !reflect.DeepEqual(d.IdealToPersonal, rd.PersonalToIdeal) {
		t.Fatalf("双方交换评分不对称: %+v %+v", d, rd)
	}
}

func TestScoreTraitsNoSemanticOrSelfMatch(t *testing.T) {
	a, b := ruleProfile(), ruleProfile()
	base, _ := Score(a, b)
	a.PersonalTraits = []string{"开朗", "温柔"}
	a.IdealTraits = []string{"温柔"}
	b.PersonalTraits = []string{"自律"}
	b.IdealTraits = []string{"外向"}
	score, d := Score(a, b)
	if score != base || d.Traits != 0 {
		t.Fatalf("语义相似或自身匹配不应加分: %+v", d)
	}
}

func TestScoreTraitsMaximumAndMissingFields(t *testing.T) {
	a, b := ruleProfile(), ruleProfile()
	a.MBTI, b.MBTI = "INFP", "ENFJ"
	names := []string{"温柔", "开朗", "自律", "善良", "认真", "耐心", "独立", "乐观", "诚实", "幽默"}
	a.PersonalTraits, a.IdealTraits = names, names
	b.PersonalTraits, b.IdealTraits = names, names
	score, d := Score(a, b)
	if score != 200 || d.Traits != 100 {
		t.Fatalf("十项双向应加100分: %d %+v", score, d)
	}
	a.PersonalTraits, b.PersonalTraits = nil, nil
	score, d = Score(a, b)
	if score != 100 || d.Traits != 0 {
		t.Fatalf("旧资料只有理想型不应加分: %d %+v", score, d)
	}
}
