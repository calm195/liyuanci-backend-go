package moderation

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// Wordlist 是正式契约的不可变快照元数据；Version只表示资源状态版本。
type Wordlist struct {
	ID              string  `json:"id"`
	Version         int64   `json:"version"`
	Kind            string  `json:"kind"`
	Status          string  `json:"status"`
	BaseEffectiveID *string `json:"base_effective_id"`
}

// WordlistDetail 只在当前角色授权后返回完整词条。
type WordlistDetail struct {
	Wordlist
	Entries []string `json:"entries"`
}

// WordlistPage 每页都包含三类一致快照的基准，不随分页遗漏。
type WordlistPage struct {
	Items      []Wordlist            `json:"items"`
	NextCursor *string               `json:"next_cursor"`
	Heads      []entity.WordlistHead `json:"heads"`
}

// WordlistInput 每次提交完整快照，ExpectedVersion是对应kind基准代次。
type WordlistInput struct {
	Kind            string   `json:"kind"`
	Entries         []string `json:"entries"`
	ExpectedVersion int64    `json:"expected_version"`
}

// WordlistPublish 同时比较草案资源和kind基准，禁止旧草案覆盖后来生效版本。
type WordlistPublish struct {
	ExpectedVersion     int64 `json:"expected_version"`
	ExpectedHeadVersion int64 `json:"expected_head_version"`
}

func wordlist(row entity.WordlistVersion) Wordlist {
	return Wordlist{row.ID, row.Version, row.Type, row.Status, row.BaseEffectiveID}
}
func readList(db *gorm.DB, id string) (row entity.WordlistVersion, err error) {
	err = db.Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}
func entries(db *gorm.DB, id string) ([]string, error) {
	out := []string{}
	e := db.Model(&entity.WordlistEntry{}).Where("version_id=?", id).Order("entry").Pluck("entry", &out).Error
	return out, e
}

// Wordlists 返回授权元数据和全部三类当前基准，游标绑定当前管理员与列表用途。
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
		if len(out.Heads) != 3 {
			return command.ErrUnavailable
		}
		var rows []entity.WordlistVersion
		q := db.Where("type IN ?", []string{"crisis", "school", "ordinary_violation"})
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
		out.Entries, e = entries(db, id)
		if e != nil {
			return e
		}
		return audit(db, actor, id, "wordlist.detail")
	})
	return
}
func validateEntries(input WordlistInput, max int) error {
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
func (s *Service) writeWordlist(ctx context.Context, token, key, rid, id string, input WordlistInput, publish WordlistPublish) (out Wordlist, err error) {
	op, path, request := "wordlist.create", "/api/v1/admin/wordlists/versions", any(input)
	if id != "" {
		op = "wordlist.publish"
		path = "/api/v1/admin/wordlists/" + id + "/publish"
		request = publish
	}
	d := command.Definition{Kind: op, Method: "POST", ResourcePath: path, ActorKind: "admin", AllowedRoles: []string{"super_admin", "review_operator"}, Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
		return command.Discovery{}, nil
	}, Authorize: func(context.Context, *command.Tx) error { return nil }}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		k := input.Kind
		var row entity.WordlistVersion
		if id != "" {
			var e error
			row, e = readList(db, id)
			if e != nil {
				return "", e
			}
			k = row.Type
		}
		var head entity.WordlistHead
		if e := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind=?", k).Take(&head).Error; e != nil {
			return "", e
		}
		if head.Version == int64(^uint64(0)>>1) {
			return "", command.ErrConflict
		}
		if id == "" {
			if head.Version != input.ExpectedVersion {
				return "", command.ErrVersionConflict
			}
			row = entity.WordlistVersion{ID: uuid.NewString(), Type: k, Version: 1, Status: "draft", BaseEffectiveID: head.EffectiveID}
			if e := db.Create(&row).Error; e != nil {
				return "", e
			}
			sorted := append([]string(nil), input.Entries...)
			slices.Sort(sorted)
			for _, entry := range sorted {
				if e := db.Create(&entity.WordlistEntry{ID: uuid.NewString(), VersionID: row.ID, Entry: entry, Action: k}).Error; e != nil {
					return "", e
				}
			}
		} else {
			if row.Version != publish.ExpectedVersion || head.Version != publish.ExpectedHeadVersion {
				return "", command.ErrVersionConflict
			}
			if row.Status != "draft" || !sameID(row.BaseEffectiveID, head.EffectiveID) {
				return "", command.ErrConflict
			}
			if head.EffectiveID != nil {
				if e := db.Model(&entity.WordlistVersion{}).Where("id=? AND status='published'", *head.EffectiveID).Updates(map[string]any{"status": "archived", "version": gorm.Expr("version+1")}).Error; e != nil {
					return "", e
				}
			}
			if e := db.Model(&entity.WordlistVersion{}).Where("id=?", id).Updates(map[string]any{"status": "published", "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
			head.EffectiveID = &row.ID
		}
		if e := db.Model(&entity.WordlistHead{}).Where("kind=?", k).Updates(map[string]any{"version": head.Version + 1, "effective_id": head.EffectiveID}).Error; e != nil {
			return "", e
		}
		if e := audit(db, tx.Actor().ID, row.ID, op); e != nil {
			return "", e
		}
		return row.ID, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		row, e := readList(tx.DB().WithContext(ctx), ref)
		out = wordlist(row)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.digest(request)})
	return
}
func sameID(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
