package matchmaking

import (
	"reflect"
	"strings"
	"testing"
)

// TestNormalizeTraits 验证输入边界、Unicode 规范化与大小写展示保留。
func TestNormalizeTraits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   []string
		want    []string
		invalid bool
	}{
		{"空列表", []string{}, []string{}, false},
		{"去重及全角", []string{" 温柔 ", "温柔", "ＩＮＦＰ"}, []string{"温柔", "INFP"}, false},
		{"空词", []string{" "}, nil, true},
		{"超长", []string{strings.Repeat("好", 21)}, nil, true},
		{"控制符", []string{"温\n柔"}, nil, true},
		{"隐形符", []string{"温\u200b柔"}, nil, true},
		{"超数", make([]string, 11), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTraits(tc.input)
			if (err != nil) != tc.invalid {
				t.Fatalf("错误=%v", err)
			}
			if err == nil && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("得到%v，期望%v", got, tc.want)
			}
		})
	}
}

// TestTraitQueryBounds 验证列表/词云共用查询的无效筛选不会降级为全量返回。
func TestTraitQueryBounds(t *testing.T) {
	negative, low, high := int64(-1), int64(1), int64(2)
	for _, q := range []TraitQuery{{Gender: "target_female"}, {Status: "all"}, {Sort: "name"}, {Page: -1}, {Limit: 101}, {MinHeat: &negative}, {MinHeat: &high, MaxHeat: &low}, {Q: strings.Repeat("长", 101)}} {
		if _, err := normalizeTraitQuery(q, 100); err == nil {
			t.Fatalf("接受无效查询：%+v", q)
		}
	}
}
