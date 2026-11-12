package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Wordlist 是正式契约的不可变快照元数据；Version只表示资源状态版本。
type Wordlist struct {
	ID              string  `json:"id"`
	Version         int64   `json:"version"`
	Kind            string  `json:"kind"`
	Status          string  `json:"status"`
	BaseEffectiveID *string `json:"base_effective_id"`
	UpstreamVersion string  `json:"upstream_version,omitempty"`
	UpstreamDigest  string  `json:"upstream_digest,omitempty"`
}

// WordlistDetail 只在当前角色授权后返回完整词条。
type WordlistDetail struct {
	Rules []WordlistRule `json:"rules,omitempty"`
	Wordlist
	Entries         []string `json:"entries"`
	DisabledEntries []string `json:"disabled_entries,omitempty"`
}

// WordlistPage 每页都包含全部一致快照的基准，不随分页遗漏。
type WordlistPage struct {
	Items      []Wordlist            `json:"items"`
	NextCursor *string               `json:"next_cursor"`
	Heads      []entity.WordlistHead `json:"heads"`
}

// WordlistInput 每次提交完整快照，ExpectedVersion是对应kind基准代次。
type WordlistInput struct {
	Rules           []WordlistRule `json:"rules,omitempty"`
	Kind            string         `json:"kind"`
	Entries         []string       `json:"entries"`
	ExpectedVersion int64          `json:"expected_version"`
	DisabledEntries []string       `json:"disabled_entries,omitempty"`
}

// WordlistPublish 同时比较草案资源和kind基准，禁止旧草案覆盖后来生效版本。
type WordlistPublish struct {
	ExpectedVersion     int64 `json:"expected_version"`
	ExpectedHeadVersion int64 `json:"expected_head_version"`
}

func wordlist(row entity.WordlistVersion) Wordlist {
	return Wordlist{
		ID: row.ID, Version: row.Version, Kind: row.Type, Status: row.Status,
		BaseEffectiveID: row.BaseEffectiveID, UpstreamVersion: row.UpstreamVersion, UpstreamDigest: row.UpstreamDigest,
	}
}

func readList(db *gorm.DB, id string) (row entity.WordlistVersion, err error) {
	err = db.Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}

func enabledEntries(db *gorm.DB, id string) ([]string, error) {
	out := []string{}
	e := db.Model(&entity.WordlistEntry{}).Where("version_id=? AND NOT disabled", id).Order("entry").Pluck("entry", &out).Error
	return out, e
}

// Wordlists 返回授权元数据和全部当前基准，游标绑定当前管理员与列表用途。
func (s *Service) Wordlists(ctx context.Context, token, rid, cursor string, limit int) (out WordlistPage, err error) {
	defer func() {
		if err != nil {
			out = WordlistPage{}
		}
	}()
	defer s.done("wordlists.list", rid, time.Now(), &err)
	if limit < 1 || limit > 100 {
		return out, command.ErrInvalid
	}
	out.Items = []Wordlist{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorize(db.Statement.Context, db, token, command.Discovery{})
		if e != nil {
			return e
		}
		after, e := s.after(actor, "wordlists", cursor)
		if e != nil {
			return e
		}
		if e = db.Order("kind").Find(&out.Heads).Error; e != nil {
			return e
		}
		if len(out.Heads) != 4 {
			return command.ErrUnavailable
		}
		var rows []entity.WordlistVersion
		q := db.Where("type IN ?", []string{"crisis", "school", "ordinary_violation", builtinKind})
		if after != "" {
			q = q.Where("id>?", after)
		}
		if e = q.Order("id").Limit(limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > limit {
			rows = rows[:limit]
			next := s.cursor(actor, "wordlists", rows[len(rows)-1].ID)
			out.NextCursor = &next
		}
		for _, row := range rows {
			out.Items = append(out.Items, wordlist(row))
		}
		return nil
	})
	return
}

// Wordlist 读取草案、生效或归档的完整快照，审计提交后才返回词条。
func (s *Service) Wordlist(ctx context.Context, token, rid, id string) (out WordlistDetail, err error) {
	defer func() {
		if err != nil {
			out = WordlistDetail{}
		}
	}()
	defer s.done("wordlists.detail", rid, time.Now(), &err)
	if !validID(id) {
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
		out.Wordlist = wordlist(row)
		if row.Type == builtinKind {
			out.Entries = []string{}
			return audit(db, actor, id, "wordlist.detail")
		}
		var rows []entity.WordlistEntry
		if e = db.Where("version_id=?", id).Order("entry").Find(&rows).Error; e != nil {
			return e
		}
		out.Entries = make([]string, 0, len(rows))
		for _, row := range rows {
			out.Entries = append(out.Entries, row.Entry)
			out.Rules = append(out.Rules, ruleFromEntry(row))
			if row.Disabled {
				out.DisabledEntries = append(out.DisabledEntries, row.Entry)
			}
		}
		return audit(db, actor, id, "wordlist.detail")
	})
	return
}

func validateEntries(input WordlistInput, max int) error {
	if err := validateWordlistRules(input); err != nil {
		return err
	}
	if !kind(input.Kind) || input.ExpectedVersion < 1 || input.Entries == nil || len(input.Entries) > max {
		return command.ErrInvalid
	}
	seen := map[string]bool{}
	for _, entry := range input.Entries {
		if !text(entry, 1000) || seen[entry] {
			return command.ErrInvalid
		}
		seen[entry] = true
	}
	for _, entry := range input.DisabledEntries {
		if !seen[entry] {
			return command.ErrInvalid
		}
		delete(seen, entry)
	}
	return nil
}

// CreateWordlist 只创建完整新草案并推进基准，不改变生效快照；同键重放重新复核角色。
func (s *Service) CreateWordlist(ctx context.Context, token, key, rid string, input WordlistInput) (out Wordlist, err error) {
	defer func() {
		if err != nil {
			out = Wordlist{}
		}
	}()
	defer s.done("wordlists.create", rid, time.Now(), &err)
	if e := validateEntries(input, s.cfg.MaxEntries); e != nil {
		return out, e
	}
	return s.writeWordlist(ctx, token, key, rid, "", input, WordlistPublish{})
}

// PublishWordlist 原子切换生效指针和归档旧版，既有扫描仍引用原快照，不触发历史重扫。
func (s *Service) PublishWordlist(ctx context.Context, token, key, rid, id string, input WordlistPublish) (out Wordlist, err error) {
	defer func() {
		if err != nil {
			out = Wordlist{}
		}
	}()
	defer s.done("wordlists.publish", rid, time.Now(), &err)
	if !validID(id) || input.ExpectedVersion < 1 || input.ExpectedHeadVersion < 1 {
		return out, command.ErrInvalid
	}
	return s.writeWordlist(ctx, token, key, rid, id, WordlistInput{}, input)
}

type wordlistChange struct {
	operation  string
	path       string
	request    any
	kind       string
	id         string
	version    int64
	publish    *WordlistPublish
	buildDraft func(*gorm.DB, entity.WordlistHead, entity.WordlistVersion) (entity.WordlistVersion, []entity.WordlistEntry, error)
}

func (s *Service) writeWordlist(ctx context.Context, token, key, rid, id string, input WordlistInput, publish WordlistPublish) (Wordlist, error) {
	if id != "" {
		return s.changeWordlist(ctx, token, key, rid, wordlistChange{
			operation: "wordlist.publish", path: "/api/v1/admin/wordlists/" + id + "/publish",
			id: id, version: publish.ExpectedHeadVersion, request: publish, publish: &publish,
		})
	}
	return s.changeWordlist(ctx, token, key, rid, wordlistChange{
		operation: "wordlist.create", path: "/api/v1/admin/wordlists/versions",
		kind: input.Kind, version: input.ExpectedVersion, request: input,
		buildDraft: func(_ *gorm.DB, _ entity.WordlistHead, _ entity.WordlistVersion) (entity.WordlistVersion, []entity.WordlistEntry, error) {
			sorted := append([]string(nil), input.Entries...)
			slices.Sort(sorted)
			disabled := make(map[string]bool, len(input.DisabledEntries))
			for _, entry := range input.DisabledEntries {
				disabled[entry] = true
			}
			entries := make([]entity.WordlistEntry, 0, len(sorted))
			for _, entry := range sorted {
				row := entity.WordlistEntry{Entry: entry, Action: input.Kind, Disabled: disabled[entry]}
				for _, rule := range input.Rules {
					if rule.Entry == entry {
						row.Category = rule.Category
						row.MatchMode = rule.MatchMode
						row.Action = rule.Action
						row.Aliases, _ = json.Marshal(rule.Aliases)
						if string(row.Aliases) == "null" {
							row.Aliases = json.RawMessage(`[]`)
						}
					}
				}
				entries = append(entries, row)
			}
			return entity.WordlistVersion{}, entries, nil
		},
	})
}

// changeWordlist 统一加锁、并发检查、幂等及审计边界。派生版本的词条始终完整复制。
func (s *Service) changeWordlist(ctx context.Context, token, key, rid string, change wordlistChange) (out Wordlist, err error) {
	d := command.Definition{
		Kind:         change.operation,
		Method:       "POST",
		ResourcePath: change.path,
		ActorKind:    "admin",
		AllowedRoles: []string{"super_admin", "review_operator"},
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(context.Context, *command.Tx) error { return nil },
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		k := change.kind
		var row entity.WordlistVersion
		if change.id != "" {
			var e error
			row, e = readList(db, change.id)
			if e != nil {
				return "", e
			}
			k = row.Type
		}
		var head entity.WordlistHead
		if e := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind=?", k).Take(&head).Error; e != nil {
			return "", e
		}
		if head.Version != change.version {
			return "", command.ErrVersionConflict
		}
		if head.Version == int64(^uint64(0)>>1) {
			return "", command.ErrConflict
		}
		// 等待分类锁期间其他发布可能归档目标，锁内按最新状态校验。
		if change.id != "" {
			var e error
			row, e = readList(db, change.id)
			if e != nil {
				return "", e
			}
		}
		if change.publish != nil {
			if e := publishWordlist(db, &head, row, *change.publish); e != nil {
				return "", e
			}
		} else {
			draft, entries, e := change.buildDraft(db, head, row)
			if e != nil {
				return "", e
			}
			draft.ID, draft.Type, draft.Version, draft.Status, draft.BaseEffectiveID = uuid.NewString(), k, 1, "draft", head.EffectiveID
			if e = createWordlistSnapshot(db, draft, entries); e != nil {
				return "", e
			}
			row = draft
		}
		if e := db.Model(&entity.WordlistHead{}).Where("kind=?", k).
			Updates(map[string]any{"version": head.Version + 1, "effective_id": head.EffectiveID}).
			Error; e != nil {
			return "", e
		}
		if e := audit(db, tx.Actor().ID, row.ID, change.operation); e != nil {
			return "", e
		}
		return row.ID, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		row, e := readList(tx.DB().WithContext(ctx), ref)
		out = wordlist(row)
		return e
	}
	_, err = s.engine.Execute(
		ctx,
		d,
		command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(change.request)},
	)
	return
}

func createWordlistSnapshot(db *gorm.DB, row entity.WordlistVersion, entries []entity.WordlistEntry) error {
	if e := db.Create(&row).Error; e != nil {
		return e
	}
	if len(entries) == 0 {
		return nil
	}
	for i := range entries {
		entries[i].ID, entries[i].VersionID = uuid.NewString(), row.ID
	}
	return db.CreateInBatches(entries, 500).Error
}

func publishWordlist(db *gorm.DB, head *entity.WordlistHead, row entity.WordlistVersion, publish WordlistPublish) error {
	if row.Version != publish.ExpectedVersion {
		return command.ErrVersionConflict
	}
	if row.Status != "draft" || !sameID(row.BaseEffectiveID, head.EffectiveID) {
		return command.ErrConflict
	}
	if e := validateWordlistMatcher(db, row); e != nil {
		return e
	}
	if head.EffectiveID != nil {
		if e := db.Model(&entity.WordlistVersion{}).Where("id=? AND status='published'", *head.EffectiveID).
			Updates(map[string]any{"status": "archived", "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
	}
	if e := db.Model(&entity.WordlistVersion{}).Where("id=?", row.ID).
		Updates(map[string]any{"status": "published", "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	head.EffectiveID = &row.ID
	return nil
}

func sameID(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
