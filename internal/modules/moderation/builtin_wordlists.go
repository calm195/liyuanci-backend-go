package moderation

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	swd "github.com/kirklin/go-swd"
	swdversion "github.com/kirklin/go-swd/version"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

const builtinKind = "builtin"

const (
	hintDisposition   = "hint"
	reviewDisposition = "review"
)

// 上游词表为 github.com/kirklin/go-swd 的 Apache-2.0 制品。
// 由 scripts/import_swd_dictionary.py 从固定依赖版本生成，保留同词不同标签。
//
//go:embed builtin_catalog.tsv
var builtinCatalog string

var loadBuiltinCatalog = sync.OnceValues(parseBuiltinCatalog)

type upstreamCatalog struct {
	version string
	digest  string
	entries []entity.WordlistEntry
}

func parseBuiltinCatalog() (upstreamCatalog, error) {
	scanner := bufio.NewScanner(strings.NewReader(builtinCatalog))
	if !scanner.Scan() || scanner.Text() != "github.com/kirklin/go-swd\t"+swdversion.Version {
		return upstreamCatalog{}, command.ErrUnavailable
	}
	actions := make(map[string]string)
	for _, label := range swd.Labels() {
		action := hintDisposition
		if label.Risk() >= swd.RiskMedium {
			action = reviewDisposition
		}
		actions[label.String()] = action
	}
	sum := sha256.Sum256([]byte(builtinCatalog))
	out := upstreamCatalog{version: swdversion.Version, digest: hex.EncodeToString(sum[:])}
	for scanner.Scan() {
		label, word, ok := strings.Cut(scanner.Text(), "\t")
		action := actions[label]
		if !ok || action == "" || !text(word, 255) {
			return upstreamCatalog{}, command.ErrUnavailable
		}
		out.entries = append(out.entries, entity.WordlistEntry{
			Entry: word, Action: action, UpstreamLabel: label, UpstreamAction: action,
		})
	}
	if err := scanner.Err(); err != nil || len(out.entries) == 0 {
		return upstreamCatalog{}, command.ErrUnavailable
	}
	return out, nil
}

// InitializeBuiltin 仅在显式初始化中发布首次上游词库；重跑时保留数据库内的已发布规则。
func InitializeBuiltin(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}).
		Transaction(func(tx *gorm.DB) error {
			var head entity.WordlistHead
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind=?", builtinKind).Take(&head).Error; e != nil {
				return e
			}
			if head.EffectiveID != nil {
				return nil
			}
			catalog, e := loadBuiltinCatalog()
			if e != nil {
				return e
			}
			row := entity.WordlistVersion{
				ID: uuid.NewString(), Type: builtinKind, Status: "published", Version: 1,
				UpstreamVersion: catalog.version, UpstreamDigest: catalog.digest,
			}
			if e = createWordlistSnapshot(tx, row, append([]entity.WordlistEntry(nil), catalog.entries...)); e != nil {
				return e
			}
			if e = validateWordlistMatcher(tx, row); e != nil {
				return e
			}
			return tx.Model(&entity.WordlistHead{}).Where("kind=?", builtinKind).
				Updates(map[string]any{"version": head.Version + 1, "effective_id": row.ID}).Error
		})
}

type builtinRule struct {
	ID          string `json:"id" gorm:"column:id"`
	Word        string `json:"word" gorm:"column:word"`
	Label       string `json:"label" gorm:"column:label"`
	Disposition string `json:"disposition" gorm:"column:disposition"`
	Enabled     bool   `json:"enabled" gorm:"column:enabled"`
}

func builtinRuleQuery(db *gorm.DB, id string) *gorm.DB {
	return db.Model(&entity.WordlistEntry{}).Where("version_id=? AND upstream_label <> ''", id)
}

const builtinRuleColumns = "id, entry AS word, upstream_label AS label, action AS disposition, NOT disabled AS enabled"

func builtinRules(db *gorm.DB, id string) ([]builtinRule, error) {
	out := []builtinRule{}
	err := builtinRuleQuery(db, id).Select(builtinRuleColumns).Order("upstream_label, entry, id").Find(&out).Error
	return out, err
}

// 限制单次读取和草稿覆盖的规模，同时容纳完整的内置词表。
const maxBuiltinWordlistEntries = 20000

// BuiltinWordlistFilter 是固定词库快照的服务端筛选条件。
type BuiltinWordlistFilter struct {
	Query       string
	Label       string
	Disposition string
	Enabled     *bool
	Offset      int
	Limit       int
}

// BuiltinWordlistPage 返回词库快照、筛选结果和其可用标签。
type BuiltinWordlistPage struct {
	Items           []builtinRule `json:"items"`
	Total           int64         `json:"total"`
	Labels          []string      `json:"labels"`
	UpstreamVersion string        `json:"upstream_version"`
}

// BuiltinWordlistEntries 返回指定内置版本的词条页。
func (s *Service) BuiltinWordlistEntries(ctx context.Context, token, rid, id string, f BuiltinWordlistFilter) (out BuiltinWordlistPage, err error) {
	defer s.done("wordlists.builtin.entries", rid, time.Now(), &err)
	if !validID(id) || f.Offset < 0 || f.Offset > 1000000 || f.Limit < 1 || f.Limit > maxBuiltinWordlistEntries ||
		!utf8.ValidString(f.Query) || utf8.RuneCountInString(f.Query) > 255 ||
		!utf8.ValidString(f.Label) || len(f.Label) > 80 || f.Disposition != "" && !validDisposition(f.Disposition) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorize(db.Statement.Context, db, token, command.Discovery{})
		if e != nil {
			return e
		}
		row, e := readList(db, id)
		if e != nil {
			return e
		}
		if row.Type != builtinKind {
			return command.ErrNotFound
		}
		out.UpstreamVersion = row.UpstreamVersion
		out.Labels = []string{}
		if e = builtinRuleQuery(db, id).Distinct("upstream_label").Order("upstream_label").Pluck("upstream_label", &out.Labels).Error; e != nil {
			return e
		}
		query := builtinRuleQuery(db, id)
		if f.Query != "" {
			query = query.Where("strpos(lower(entry), lower(?)) > 0", f.Query)
		}
		if f.Label != "" {
			query = query.Where("upstream_label=?", f.Label)
		}
		if f.Disposition != "" {
			query = query.Where("action=?", f.Disposition)
		}
		if f.Enabled != nil {
			query = query.Where("disabled=?", !*f.Enabled)
		}
		if e = query.Count(&out.Total).Error; e != nil {
			return e
		}
		out.Items = []builtinRule{}
		if e = query.Select(builtinRuleColumns).Order("upstream_label, entry, id").Offset(f.Offset).Limit(f.Limit).Find(&out.Items).Error; e != nil {
			return e
		}
		return audit(db, actor, id, "wordlist.builtin.entries")
	})
	return
}

func validDisposition(s string) bool { return s == hintDisposition || s == reviewDisposition }

// BuiltinWordlistOverride 是基于当前生效版本的单词或标签覆盖。
type BuiltinWordlistOverride struct {
	ID          string  `json:"id,omitempty"`
	Label       string  `json:"label,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	Disposition *string `json:"disposition,omitempty"`
}

// BuiltinWordlistInput 以生效版本为基准创建完整草案。单词覆盖优先于同批标签覆盖。
type BuiltinWordlistInput struct {
	ExpectedVersion int64                     `json:"expected_version"`
	Overrides       []BuiltinWordlistOverride `json:"overrides,omitempty"`
	LabelOverrides  []BuiltinWordlistOverride `json:"label_overrides,omitempty"`
	RefreshUpstream bool                      `json:"refresh_upstream,omitempty"`
}

func validateOverrides(items []BuiltinWordlistOverride, byLabel bool) error {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		id := item.ID
		if byLabel {
			if id != "" || !text(item.Label, 80) {
				return command.ErrInvalid
			}
			id = item.Label
		} else if !validID(id) || item.Label != "" {
			return command.ErrInvalid
		}
		if seen[id] || item.Enabled == nil && item.Disposition == nil ||
			item.Disposition != nil && !validDisposition(*item.Disposition) {
			return command.ErrInvalid
		}
		seen[id] = true
	}
	return nil
}

// CreateBuiltinWordlist 新建内置覆盖或上游更新草案，保持原生效快照不变。
func (s *Service) CreateBuiltinWordlist(ctx context.Context, token, key, rid string, input BuiltinWordlistInput) (out Wordlist, err error) {
	defer s.done("wordlists.builtin.create", rid, time.Now(), &err)
	if input.ExpectedVersion < 1 || len(input.Overrides) > maxBuiltinWordlistEntries || len(input.LabelOverrides) > 100 ||
		len(input.Overrides)+len(input.LabelOverrides) == 0 && !input.RefreshUpstream ||
		validateOverrides(input.Overrides, false) != nil || validateOverrides(input.LabelOverrides, true) != nil {
		return out, command.ErrInvalid
	}
	if input.RefreshUpstream && len(input.Overrides)+len(input.LabelOverrides) != 0 {
		return out, command.ErrInvalid
	}
	return s.changeWordlist(ctx, token, key, rid, wordlistChange{
		operation: "wordlist.builtin.create", path: "/api/v1/admin/wordlists/builtin/versions",
		kind: builtinKind, version: input.ExpectedVersion, request: input,
		buildDraft: func(db *gorm.DB, head entity.WordlistHead, _ entity.WordlistVersion) (entity.WordlistVersion, []entity.WordlistEntry, error) {
			return buildBuiltinDraft(db, head, input)
		},
	})
}

func buildBuiltinDraft(db *gorm.DB, head entity.WordlistHead, input BuiltinWordlistInput) (entity.WordlistVersion, []entity.WordlistEntry, error) {
	if head.EffectiveID == nil {
		return entity.WordlistVersion{}, nil, command.ErrUnavailable
	}
	base, e := readList(db, *head.EffectiveID)
	if e != nil {
		return base, nil, e
	}
	entries, e := readWordlistEntries(db, base.ID)
	if e != nil {
		return base, nil, e
	}
	draft := entity.WordlistVersion{UpstreamVersion: base.UpstreamVersion, UpstreamDigest: base.UpstreamDigest}
	if input.RefreshUpstream {
		catalog, e := loadBuiltinCatalog()
		if e != nil {
			return draft, nil, e
		}
		if catalog.digest == base.UpstreamDigest {
			return draft, nil, command.ErrConflict
		}
		draft.UpstreamVersion, draft.UpstreamDigest = catalog.version, catalog.digest
		updated, e := carryBuiltinOverrides(catalog.entries, entries)
		return draft, updated, e
	}
	if e = applyBuiltinOverrides(entries, input); e != nil {
		return draft, nil, e
	}
	return draft, entries, nil
}

func applyBuiltinOverrides(entries []entity.WordlistEntry, input BuiltinWordlistInput) error {
	byID := make(map[string]BuiltinWordlistOverride, len(input.Overrides))
	byLabel := make(map[string]BuiltinWordlistOverride, len(input.LabelOverrides))
	appliedLabels := make(map[string]bool, len(input.LabelOverrides))
	for _, override := range input.Overrides {
		byID[override.ID] = override
	}
	for _, override := range input.LabelOverrides {
		byLabel[override.Label] = override
	}
	for i := range entries {
		entry := &entries[i]
		if override, ok := byLabel[entry.UpstreamLabel]; ok {
			applyBuiltinOverride(entry, override)
			appliedLabels[entry.UpstreamLabel] = true
		}
		if override, ok := byID[entry.ID]; ok {
			applyBuiltinOverride(entry, override)
			delete(byID, entry.ID)
		}
	}
	if len(byID) != 0 || len(appliedLabels) != len(byLabel) {
		return command.ErrInvalid
	}
	return nil
}

func applyBuiltinOverride(entry *entity.WordlistEntry, override BuiltinWordlistOverride) {
	if override.Enabled != nil {
		entry.Disabled = !*override.Enabled
	}
	if override.Disposition != nil {
		entry.Action = *override.Disposition
	}
}

type builtinEntryKey struct{ word, label string }

func builtinEntriesByKey(entries []entity.WordlistEntry) map[builtinEntryKey]entity.WordlistEntry {
	byKey := make(map[builtinEntryKey]entity.WordlistEntry, len(entries))
	for _, entry := range entries {
		byKey[builtinEntryKey{entry.Entry, entry.UpstreamLabel}] = entry
	}
	return byKey
}

func hasBuiltinOverride(entry entity.WordlistEntry) bool {
	return entry.Disabled || entry.Action != entry.UpstreamAction
}

func carryBuiltinOverrides(next, old []entity.WordlistEntry) ([]entity.WordlistEntry, error) {
	byKey := builtinEntriesByKey(old)
	out := append([]entity.WordlistEntry(nil), next...)
	for i := range out {
		entry := &out[i]
		key := builtinEntryKey{entry.Entry, entry.UpstreamLabel}
		if prior, ok := byKey[key]; ok {
			delete(byKey, key)
			entry.Disabled = prior.Disabled
			if prior.Action != prior.UpstreamAction {
				entry.Action = prior.Action
			}
		}
	}
	// 上游删除了本地管理过的词条时，要求管理员先审阅并处理覆盖。
	for _, removed := range byKey {
		if hasBuiltinOverride(removed) {
			return nil, command.ErrConflict
		}
	}
	return out, nil
}

func readWordlistEntries(db *gorm.DB, id string) ([]entity.WordlistEntry, error) {
	var out []entity.WordlistEntry
	e := db.Where("version_id=?", id).Order("upstream_label, entry").Find(&out).Error
	return out, e
}
