package randomprofile

import (
	"strings"
	"testing"
)

func TestDrawPreviousAndAvailability(t *testing.T) {
	a := Avatar{ID: "one", ImagePath: "/one"}
	b := Avatar{ID: "two", ImagePath: "/two"}
	catalog := Catalog{
		Avatars: []Avatar{a, b}, Adjectives: []Word{{Text: "静静"}, {Text: "温柔"}},
		Nouns: []Word{{Text: "荷叶"}, {Text: "小亭"}},
	}
	for range 40 {
		out, err := pick(catalog, DrawInput{PreviousAvatarID: a.ID})
		if err != nil || out.AvatarID != b.ID || !out.CanReroll || !strings.Contains(out.Nickname, "的") {
			t.Fatalf("多头像抽取有重复，或未正确拼接：%+v %v", out, err)
		}
	}
	catalog.Avatars = []Avatar{a}
	for range 40 {
		out, err := pick(catalog, DrawInput{PreviousAvatarID: a.ID, PreviousNickname: "静静的荷叶"})
		if err != nil || out.AvatarID != a.ID || !out.CanReroll || out.Nickname == "静静的荷叶" {
			t.Fatalf("单头像有可替换昵称时重复结果：%+v %v", out, err)
		}
	}
	catalog.Adjectives, catalog.Nouns = catalog.Adjectives[:1], catalog.Nouns[:1]
	out, err := pick(catalog, DrawInput{PreviousAvatarID: a.ID, PreviousNickname: "静静的荷叶"})
	if err != nil || out.CanReroll || out.Nickname != "静静的荷叶" {
		t.Fatalf("唯一组合应仍可读：%+v %v", out, err)
	}
	catalog.Adjectives = nil
	if _, err := pick(catalog, DrawInput{}); err == nil {
		t.Fatal("空池未提供依赖错误")
	}
}

func TestDistinctWordsAvoidAmbiguousParts(t *testing.T) {
	adj := []Word{{Text: "暖"}, {Text: "暖的风"}}
	nouns := []Word{{Text: "风的旅人"}, {Text: "旅人"}}
	for range 30 {
		a, b, ok := pickDifferentWords(adj, nouns, "暖的风的旅人")
		if !ok || adj[a].Text+"的"+nouns[b].Text == "暖的风的旅人" {
			t.Fatal("分隔位置不同的同一昵称仍被重新抽中")
		}
	}
}
