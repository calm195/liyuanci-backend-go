package moderation_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
)

// verifyWordlistRuleRestore 经正式历史恢复与生产匹配器验证新规则元数据没有退化为普通关键词。
func verifyWordlistRuleRestore(t *testing.T, service *moderation.Service, token string, head func(string) entity.WordlistHead, publish func(string, []string) moderation.Wordlist) {
	t.Run("历史恢复保留缩写边界提示及停用状态", func(t *testing.T) {
		ctx := context.Background()
		must := func(e error) {
			t.Helper()
			if e != nil {
				t.Fatal(e)
			}
		}
		h := head("ordinary_violation")
		original, e := service.Wordlist(ctx, token, "", *h.EffectiveID)
		must(e)
		rules := []moderation.WordlistRule{
			{Entry: "合成提示词", Category: "abbreviation", Aliases: []string{"restorehint"}, MatchMode: "word", Action: "hint"},
			{Entry: "合成停用词", Category: "negative_expression", Aliases: []string{"restoredisabled"}, MatchMode: "contains", Action: "review"},
		}
		source, e := service.CreateWordlist(ctx, token, uuid.NewString(), "", moderation.WordlistInput{
			Kind: h.Kind, ExpectedVersion: h.Version,
			Entries:         append(append([]string(nil), original.Entries...), "合成提示词", "合成停用词"),
			DisabledEntries: []string{"合成停用词"}, Rules: rules,
		})
		must(e)
		_, e = service.PublishWordlist(ctx, token, uuid.NewString(), "", source.ID, moderation.WordlistPublish{ExpectedVersion: source.Version, ExpectedHeadVersion: head(h.Kind).Version})
		must(e)
		publish(h.Kind, original.Entries)
		restored, e := service.RestoreWordlist(ctx, token, uuid.NewString(), "", source.ID, moderation.WordlistRestore{ExpectedHeadVersion: head(h.Kind).Version})
		must(e)
		detail, e := service.Wordlist(ctx, token, "", restored.ID)
		must(e)
		for _, rule := range rules {
			found := false
			for _, actual := range detail.Rules {
				if actual.Entry == rule.Entry {
					found = reflect.DeepEqual(actual, rule)
				}
			}
			if !found {
				t.Fatal("恢复丢失规则元数据", detail.Rules)
			}
		}
		if !reflect.DeepEqual(detail.DisabledEntries, []string{"合成停用词"}) {
			t.Fatal("恢复丢失停用状态", detail.DisabledEntries)
		}
		for _, tc := range []struct {
			body string
			hit  bool
		}{{"restorehint", true}, {"xrestorehint", false}, {"restoredisabled", false}} {
			preview, e := service.PreviewWordlist(ctx, token, "", restored.ID, tc.body)
			must(e)
			found := false
			for _, match := range preview.Draft.Matches {
				if match.Kind == h.Kind {
					found = true
					if match.Disposition != "hint" || match.MatchMode != "word" || match.Category != "abbreviation" {
						t.Fatal("恢复后的生产匹配行为改变", match)
					}
				}
			}
			if found != tc.hit {
				t.Fatal("恢复后词边界或停用行为改变", tc.body, preview)
			}
		}
		_, e = service.PublishWordlist(ctx, token, uuid.NewString(), "", restored.ID, moderation.WordlistPublish{ExpectedVersion: restored.Version, ExpectedHeadVersion: head(h.Kind).Version})
		must(e)
		publish(h.Kind, original.Entries)
	})
}
