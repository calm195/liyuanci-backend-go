package moduleguide

import (
	"strings"
	"testing"
)

func TestModulesValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		modules []Module
		valid   bool
	}{
		{name: "空列表不可发布"},
		{name: "正常模块", modules: []Module{{Module: "wishes", Title: "许愿池", Body: "填写你的心愿。"}}, valid: true},
		{name: "重复模块", modules: []Module{{Module: "wishes", Title: "许愿", Body: "说明"}, {Module: "wishes", Title: "重复", Body: "说明"}}},
		{name: "正文空白", modules: []Module{{Module: "wishes", Title: "许愿池", Body: " "}}},
		{name: "标题按字符限制", modules: []Module{{Module: "wishes", Title: strings.Repeat("🌞", 40), Body: "说明"}}, valid: true},
		{name: "标题过长", modules: []Module{{Module: "wishes", Title: strings.Repeat("好", 41), Body: "说明"}}},
		{name: "非法模块标识", modules: []Module{{Module: "../wishes", Title: "许愿池", Body: "说明"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validModules(test.modules); got != test.valid {
				t.Fatalf("校验结果=%v，期望%v", got, test.valid)
			}
		})
	}
}
