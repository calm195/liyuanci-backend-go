package moderation

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// WordlistRule 为自定义词条补充运营分类、显式变体与命中处置；类别不改变审核路由。
type WordlistRule struct {
	Entry     string   `json:"entry"`
	Category  string   `json:"category"`
	Aliases   []string `json:"aliases"`
	MatchMode string   `json:"match_mode"`
	Action    string   `json:"action"`
}

func validateWordlistRules(in WordlistInput) error {
	seen := map[string]bool{}
	for _, rule := range in.Rules {
		if in.Kind != "ordinary_violation" && rule.Action == "hint" {
			return command.ErrInvalid
		}
		if seen[rule.Entry] || !slices.Contains(in.Entries, rule.Entry) || !slices.Contains([]string{"", "internet_slang", "insult", "negative_expression", "abbreviation"}, rule.Category) || !slices.Contains([]string{"contains", "word"}, rule.MatchMode) || !slices.Contains([]string{"hint", "review"}, rule.Action) || len(rule.Aliases) > 20 {
			return command.ErrInvalid
		}
		seen[rule.Entry] = true
		aliases := map[string]bool{}
		for _, a := range rule.Aliases {
			if !text(a, 100) || strings.TrimSpace(a) != a || aliases[a] || a == rule.Entry {
				return command.ErrInvalid
			}
			aliases[a] = true
		}
	}
	return nil
}
func ruleFromEntry(e entity.WordlistEntry) WordlistRule {
	action := e.Action
	if action != "hint" && action != "review" {
		action = "review"
	}
	mode := e.MatchMode
	if mode == "" {
		mode = "contains"
	}
	aliases := []string{}
	if len(e.Aliases) > 0 {
		_ = json.Unmarshal(e.Aliases, &aliases)
	}
	return WordlistRule{Entry: e.Entry, Category: e.Category, Aliases: aliases, MatchMode: mode, Action: action}
}
func readCustomRules(db *gorm.DB, id string) ([]WordlistRule, error) {
	var rows []entity.WordlistEntry
	if e := db.Where("version_id=? AND NOT disabled", id).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := []WordlistRule{}
	for _, r := range rows {
		out = append(out, ruleFromEntry(r))
	}
	return out, nil
}
func wordBoundary(body string, start, end int) bool {
	adjacent := func(r rune) bool {
		return unicode.IsDigit(r) || unicode.IsLetter(r) && !unicode.Is(unicode.Han, r) || r == '_'
	}
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(body[:start])
		if adjacent(r) {
			return false
		}
	}
	if end < len(body) {
		r, _ := utf8.DecodeRuneInString(body[end:])
		if adjacent(r) {
			return false
		}
	}
	return true
}
