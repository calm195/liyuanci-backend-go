package moderation

import (
	"cmp"
	"context"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type builtinUpstreamChange struct {
	Word            string `json:"word"`
	Label           string `json:"label"`
	Change          string `json:"change"`
	Conflict        bool   `json:"conflict"`
	PreviousDefault string `json:"previous_default,omitempty"`
	NextDefault     string `json:"next_default,omitempty"`
}

// BuiltinUpstreamChangesPage 仅展示安装制品与指定词库的上游差异。
type BuiltinUpstreamChangesPage struct {
	FromVersion    string                  `json:"from_version"`
	ToVersion      string                  `json:"to_version"`
	Added          int                     `json:"added"`
	Removed        int                     `json:"removed"`
	DefaultChanged int                     `json:"default_changed"`
	Conflicts      int                     `json:"conflicts"`
	Total          int                     `json:"total"`
	Items          []builtinUpstreamChange `json:"items"`
}

// BuiltinUpstreamChanges 让有权限的管理员在创建升级草稿前检查上游差异。
func (s *Service) BuiltinUpstreamChanges(ctx context.Context, token, rid, id string, offset, limit int) (out BuiltinUpstreamChangesPage, err error) {
	defer s.done("wordlists.builtin.upstream_changes", rid, time.Now(), &err)
	if !validID(id) || offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
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
		catalog, e := loadBuiltinCatalog()
		if e != nil {
			return e
		}
		out.FromVersion, out.ToVersion = row.UpstreamVersion, catalog.version
		out.Items = []builtinUpstreamChange{}
		if row.UpstreamDigest != catalog.digest {
			entries, e := readWordlistEntries(db, id)
			if e != nil {
				return e
			}
			out.addChanges(catalog.entries, entries, offset, limit)
		}
		return audit(db, actor, id, "wordlist.builtin.upstream_changes")
	})
	return
}

func (p *BuiltinUpstreamChangesPage) addChanges(next, current []entity.WordlistEntry, offset, limit int) {
	before := builtinEntriesByKey(current)
	var changes []builtinUpstreamChange
	for _, entry := range next {
		key := builtinEntryKey{entry.Entry, entry.UpstreamLabel}
		prior, exists := before[key]
		delete(before, key)
		change := builtinUpstreamChange{Word: key.word, Label: key.label, NextDefault: entry.UpstreamAction}
		switch {
		case !exists:
			change.Change = "added"
			p.Added++
		case prior.UpstreamAction != entry.UpstreamAction:
			change.Change, change.PreviousDefault = "default_changed", prior.UpstreamAction
			p.DefaultChanged++
		default:
			continue
		}
		changes = append(changes, change)
	}
	for key, entry := range before {
		conflict := hasBuiltinOverride(entry)
		if conflict {
			p.Conflicts++
		}
		p.Removed++
		changes = append(changes, builtinUpstreamChange{
			Word: key.word, Label: key.label, Change: "removed", Conflict: conflict, PreviousDefault: entry.UpstreamAction,
		})
	}
	slices.SortFunc(changes, func(a, b builtinUpstreamChange) int {
		if a.Conflict != b.Conflict {
			if a.Conflict {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.Label, b.Label), cmp.Compare(a.Word, b.Word))
	})
	p.Total = len(changes)
	if offset < p.Total {
		p.Items = changes[offset:min(offset+limit, p.Total)]
	}
}
