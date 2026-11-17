package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMatchmakingCategoryNameKeys(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"分类键允许大写词条", `{"profile":{"personal_trait_categories":{"ENFP":"personality"},"ideal_trait_categories":{"CityWalk":"lifestyle"}}}`, true},
		{"字段仍须小写", `{"Profile":{}}`, false},
		{"嵌套字段仍须小写", `{"profile":{"Personal_trait_categories":{}}}`, false},
		{"分类重复键仍拒绝", `{"profile":{"personal_trait_categories":{"ENFP":"personality","ENFP":"interest"}}}`, false},
		{"普通字段重复拒绝", `{"category":"interest","category":"lifestyle"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := matchmakingJSONValue(json.NewDecoder(strings.NewReader(tc.body)))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}
