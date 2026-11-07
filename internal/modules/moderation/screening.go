package moderation

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	swd "github.com/kirklin/go-swd"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

const (
	wordlistDetectorVersion = "wordlist-first-v1"
	wordlistEngineVersion   = "go-swd/v0.3.0-policy-v1"
	wordlistMatchLimit      = 96
)

// WordlistMatch 描述原文字段内命中的词条，位置为 UTF-16 半开区间。
type WordlistMatch struct {
	Word        string `json:"word"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	Disposition string `json:"disposition"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Field       string `json:"field,omitempty"`
}

// WordlistEvaluation 保留各来源的证据及完整流转分类，证据截断不影响审核决定。
type WordlistEvaluation struct {
	Matches   []WordlistMatch `json:"matches"`
	Routes    []string        `json:"routes"`
	Truncated bool            `json:"truncated,omitempty"`
}

func (v WordlistEvaluation) routesTo(route string) bool {
	return slices.Contains(v.Routes, route)
}

type compiledWordlist struct {
	engine *swd.Engine
	rules  map[string][]WordlistMatch
	exact  []WordlistMatch
}

type wordlistMatchers struct {
	lists []compiledWordlist
}

type wordlistCache struct {
	mu       sync.Mutex
	versions [4]string
	matcher  *wordlistMatchers
}

func (r rules) versionIDs() [4]string {
	return [4]string{r.builtin.ID, r.crisis.ID, r.school.ID, r.ordinary.ID}
}

func (c *wordlistCache) lookup(snapshot rules) *wordlistMatchers {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.versions == snapshot.versionIDs() {
		return c.matcher
	}
	return nil
}

func (c *wordlistCache) compile(snapshot rules) (*wordlistMatchers, error) {
	if snapshot.matcher != nil {
		return snapshot.matcher, nil
	}
	versions := snapshot.versionIDs()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.matcher != nil && c.versions == versions && versions[1] != "" {
		return c.matcher, nil
	}
	matcher, err := compileWordlists(snapshot)
	if err == nil {
		c.versions, c.matcher = versions, matcher
	}
	return matcher, err
}

func compileWordlists(snapshot rules) (*wordlistMatchers, error) {
	out := &wordlistMatchers{}
	for _, list := range []struct {
		kind  string
		words []string
	}{
		{"crisis", snapshot.crisisEntries},
		{"school", snapshot.schoolEntries},
		{"ordinary_violation", snapshot.ordinaryEntries},
	} {
		entries := make([]WordlistMatch, 0, len(list.words))
		for _, word := range list.words {
			entries = append(entries, WordlistMatch{Word: word, Source: "custom", Kind: list.kind, Disposition: reviewDisposition})
		}
		matcher, err := compileWordlist(entries)
		if err != nil {
			return nil, err
		}
		out.lists = append(out.lists, matcher)
	}
	builtin := make([]WordlistMatch, 0, len(snapshot.builtinEntries))
	for _, entry := range snapshot.builtinEntries {
		if entry.Enabled {
			builtin = append(builtin, WordlistMatch{
				Word: entry.Word, Source: "builtin", Kind: builtinKind, Label: entry.Label, Disposition: entry.Disposition,
			})
		}
	}
	matcher, err := compileWordlist(builtin)
	if err != nil {
		return nil, err
	}
	out.lists = append(out.lists, matcher)
	return out, nil
}

func compileWordlist(entries []WordlistMatch) (compiledWordlist, error) {
	out := compiledWordlist{rules: make(map[string][]WordlistMatch)}
	patterns := make(map[string]swd.Category)
	for _, entry := range entries {
		// 旧词库允许带首尾空格和超过引擎上限的词，继续按原文字面匹配。
		if strings.TrimSpace(entry.Word) != entry.Word || utf8.RuneCountInString(entry.Word) > 255 {
			out.exact = append(out.exact, entry)
			continue
		}
		patterns[entry.Word] = swd.Custom
	}
	if len(patterns) == 0 {
		return out, nil
	}
	engine, err := swd.New(swd.WithoutDefaultDict(), swd.WithWords(patterns))
	if err != nil {
		return out, err
	}
	out.engine = engine

	// 引擎将归一化后相同的词合并到一个代表词。以每个词自匹配时最长的
	// 命中建立反向映射，保留原词、来源及全部类别。
	representatives := make(map[string]string, len(patterns))
	for _, word := range slices.Sorted(maps.Keys(patterns)) {
		longest := 0
		for match := range engine.Matches(word) {
			if length := match.ByteEnd - match.ByteStart; length > longest {
				representatives[word], longest = match.Word, length
			}
		}
	}
	for _, entry := range entries {
		if _, ok := patterns[entry.Word]; !ok {
			continue
		}
		if representative := representatives[entry.Word]; representative != "" {
			out.rules[representative] = append(out.rules[representative], entry)
		} else {
			out.exact = append(out.exact, entry)
		}
	}
	return out, nil
}

// scanWordlists 让试跑使用与 worker 相同的词条和匹配规则。
func scanWordlists(body string, snapshot rules) (WordlistEvaluation, error) {
	matcher, err := compileWordlists(snapshot)
	if err != nil {
		return WordlistEvaluation{}, err
	}
	return matcher.scan(body), nil
}

func (r rules) scan(body string) (WordlistEvaluation, error) {
	if r.matcher == nil {
		return scanWordlists(body, r)
	}
	return r.matcher.scan(body), nil
}

func (m *wordlistMatchers) scan(body string) WordlistEvaluation {
	out := WordlistEvaluation{Matches: []WordlistMatch{}, Routes: []string{}}
	offsets := make([]int, len(body)+1)
	position := 0
	for at, r := range body {
		offsets[at] = position
		position++
		if r > 0xffff {
			position++
		}
	}
	offsets[len(body)] = position
	record := func(entry WordlistMatch, start, end int) {
		route := entry.Kind
		if entry.Kind == builtinKind {
			route = "ordinary_violation"
		}
		if entry.Disposition == reviewDisposition && !out.routesTo(route) {
			out.Routes = append(out.Routes, route)
		}
		if len(out.Matches) == wordlistMatchLimit {
			out.Truncated = true
			return
		}
		entry.Start, entry.End = offsets[start], offsets[end]
		out.Matches = append(out.Matches, entry)
	}
	for _, list := range m.lists {
		if list.engine != nil {
			for match := range list.engine.Matches(body) {
				for _, entry := range list.rules[match.Word] {
					record(entry, match.ByteStart, match.ByteEnd)
				}
			}
		}
		for _, entry := range list.exact {
			for at := 0; at < len(body); {
				next := strings.Index(body[at:], entry.Word)
				if next < 0 {
					break
				}
				start := at + next
				record(entry, start, start+len(entry.Word))
				_, width := utf8.DecodeRuneInString(body[start:])
				at = start + width
			}
		}
	}
	return out
}

// WordlistEvidence 保存加密候选内的命中证据及本次扫描的版本来源。
type WordlistEvidence struct {
	EngineVersion   string          `json:"engine_version"`
	BuiltinVersion  string          `json:"builtin_version,omitempty"`
	CrisisVersion   string          `json:"crisis_version"`
	SchoolVersion   string          `json:"school_version"`
	OrdinaryVersion string          `json:"ordinary_version"`
	Matches         []WordlistMatch `json:"matches"`
	Truncated       bool            `json:"truncated,omitempty"`
}

func (r rules) evidence() *WordlistEvidence {
	return &WordlistEvidence{
		EngineVersion: wordlistEngineVersion, BuiltinVersion: r.builtin.ID,
		CrisisVersion: r.crisis.ID, SchoolVersion: r.school.ID, OrdinaryVersion: r.ordinary.ID,
		Matches: []WordlistMatch{},
	}
}

func (e *WordlistEvidence) add(field string, evaluation *WordlistEvaluation) {
	if evaluation == nil {
		return
	}

	e.Truncated = e.Truncated || evaluation.Truncated
	for _, match := range evaluation.Matches {
		if len(e.Matches) == wordlistMatchLimit {
			e.Truncated = true
			return
		}
		match.Field = field
		e.Matches = append(e.Matches, match)
	}
}

// readWordlistEvidence 复用已授权案件的加密候选，不重新扫描或读取当前词库。
func readWordlistEvidence(ctx context.Context, codec *securedata.Codec, purpose string, scan entity.ModerationScan) (*WordlistEvidence, error) {
	var projection struct {
		WordlistEvidence *WordlistEvidence
	}
	err := decryptEvidence(ctx, codec, purpose, scan.TaskID, scan.ProjectionCipher, &projection)
	return projection.WordlistEvidence, err
}
