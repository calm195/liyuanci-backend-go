package whisper

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
)

// BlockSource 只接受当前可访问公开来源，不接受匿名身份或目标账号的反查。
type BlockSource struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	ContentVersion *int64 `json:"content_version,omitempty"`
}

// BlockInput 是匿名屏蔽的最小请求。
type BlockInput struct {
	Source BlockSource `json:"source"`
}

// BlockAck 不披露条目、目标或此前是否已经屏蔽。
type BlockAck struct {
	Accepted bool `json:"accepted"`
}

// BlockEntry 只公开随机条目与乐观版本，所有来源使用相同标签。
type BlockEntry struct {
	BlockID        string   `json:"block_id"`
	Version        int64    `json:"version"`
	Label          string   `json:"label"`
	AllowedActions []string `json:"allowed_actions"`
}

func opaqueReady(ctx context.Context, db *gorm.DB) error {
	enabled, e := blockpolicy.Enabled(ctx, db)
	if e != nil {
		return e
	}
	if !enabled {
		return command.ErrUnavailable
	}
	return nil
}

func (s *Service) blockTarget(ctx context.Context, db *gorm.DB, src BlockSource) (target string, p command.Discovery, err error) {
	switch src.Kind {
	case "public_profile":
		var a entity.Account
		err = missingRow(db.WithContext(ctx).Select("id").Where("id=? AND lifecycle='active'", src.ID).Take(&a).Error)
		return a.ID, command.Discovery{Accounts: []string{a.ID}}, err
	case "whisper_post":
		if !s.config.Enabled {
			return "", p, ErrModuleUnavailable
		}
		p, err = interactionPlan(ctx, db, src.ID, "")
		if err != nil {
			return
		}
		var a entity.WhisperPost
		err = db.WithContext(ctx).Where("id=?", src.ID).Take(&a).Error
		return a.OwnerID, p, err
	case "whisper_comment":
		if !s.config.Enabled {
			return "", p, ErrModuleUnavailable
		}
		p, err = interactionPlan(ctx, db, "", src.ID)
		if err != nil {
			return
		}
		var a entity.WhisperComment
		err = db.WithContext(ctx).Where("id=?", src.ID).Take(&a).Error
		return a.AuthorID, p, err
	}
	return "", p, command.ErrInvalid
}

func (s *Service) blockVisible(ctx context.Context, tx *command.Tx, src BlockSource) error {
	if e := opaqueReady(ctx, tx.DB()); e != nil {
		return e
	}
	if src.Kind == "public_profile" {
		_, _, e := s.blockTarget(ctx, tx.DB(), src)
		return e
	}
	catalog, e := s.resources(ctx)
	if e != nil {
		return e
	}
	viewer := audience{ID: tx.Actor().ID}
	if src.Kind == "whisper_post" {
		a, e := loadPost(ctx, tx.DB(), src.ID)
		if e != nil {
			return e
		}
		if a.post.CurrentContentVersion != *src.ContentVersion {
			return command.ErrNotFound
		}
		_, e = s.publicPostBase(ctx, tx.DB(), tx.Now(), src.ID, viewer, catalog)
		return e
	}
	a, e := loadComment(ctx, tx.DB(), src.ID)
	if e != nil {
		return e
	}
	if a.comment.PostID == nil || a.comment.ContentVersion != *src.ContentVersion {
		return command.ErrNotFound
	}
	if _, e = s.publicPostBase(ctx, tx.DB(), tx.Now(), *a.comment.PostID, viewer, catalog); e != nil {
		return e
	}
	parent, e := loadPost(ctx, tx.DB(), *a.comment.PostID)
	if e != nil {
		return e
	}
	_, e = s.publicComment(ctx, tx.DB(), tx.Now(), src.ID, parent, viewer, catalog)
	return e
}

// CreateBlock 在双方及完整关系锁下解析来源，只以随机引用保存回执。
func (s *Service) CreateBlock(ctx context.Context, token, key, rid string, in BlockInput) (out BlockAck, err error) {
	defer s.completed("interaction_block_create", rid, time.Now(), &err)
	src := in.Source
	if !ValidID(src.ID) || (src.Kind == "public_profile" && src.ContentVersion != nil) || (src.Kind != "public_profile" && (src.Kind != "whisper_post" && src.Kind != "whisper_comment" || src.ContentVersion == nil || *src.ContentVersion < 1)) {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "interaction_block.create", Method: "POST", ResourcePath: "/api/v1/me/interaction-blocks", ActorKind: "student", Access: command.Ordinary, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, a command.Subject) (command.Discovery, error) {
		target, p, e := s.blockTarget(ctx, db, src)
		if e != nil {
			return p, e
		}
		q, e := safety.DiscoverBlockEffects(ctx, db, a.ID, target)
		q.Accounts = append(q.Accounts, p.Accounts...)
		q.Resources = append(q.Resources, p.Resources...)
		return q, e
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		target, _, e := s.blockTarget(ctx, tx.DB(), src)
		if e != nil {
			return e
		}
		if target == tx.Actor().ID {
			return command.ErrInvalid
		}
		return s.blockVisible(ctx, tx, src)
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		target, _, e := s.blockTarget(ctx, tx.DB(), src)
		if e != nil {
			return "", e
		}
		b := entity.Block{BlockID: uuid.NewString(), OwnerID: tx.Actor().ID, TargetID: target, Version: 1}
		if e = tx.DB().WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&b).Error; e != nil {
			return "", e
		}
		if e = tx.DB().WithContext(ctx).Where("owner_id=? AND target_id=?", tx.Actor().ID, target).Take(&b).Error; e != nil {
			return "", e
		}
		return b.BlockID, safety.ApplyBlockEffects(ctx, tx, tx.Actor().ID, target)
	}
	d.Resolve = func(context.Context, *command.Tx, string) error { out.Accepted = true; return nil }
	raw, _ := json.Marshal(in)
	defer clear(raw)
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: keyed(s.config.ReceiptKey, "interaction_block.create", raw)})
	if err != nil {
		out = BlockAck{}
	}
	return
}

// ListBlocks 包括历史来源；签名游标只携随机条目，不携目标账号。
func (s *Service) ListBlocks(ctx context.Context, token, rid string, q Query) (out Page[BlockEntry], err error) {
	defer s.completed("interaction_blocks_list", rid, time.Now(), &err)
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		a, e := reader(ctx, db, token, now, false)
		if e != nil {
			return e
		}
		if a.Restricted {
			return command.ErrForbidden
		}
		if e = opaqueReady(ctx, db); e != nil {
			return e
		}
		scope := s.scope("interaction_blocks", a.ID)
		after, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		query := db.WithContext(ctx).Where("owner_id=?", a.ID)
		if after.ID != "" {
			query = query.Where("(created_at,block_id)<(?,?::uuid)", after.At, after.ID)
		}
		var rows []entity.Block
		if e = query.Order("created_at DESC,block_id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []BlockEntry{}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			last := rows[len(rows)-1]
			v := s.encodeCursor(cursor{scope, last.BlockID, last.CreatedAt})
			out.NextCursor = &v
		}
		for _, b := range rows {
			out.Items = append(out.Items, BlockEntry{b.BlockID, b.Version, "已屏蔽对象", []string{"interactionBlockDelete"}})
		}
		return nil
	})
	if err != nil {
		out = Page[BlockEntry]{}
	}
	return
}

// DeleteBlock 只使用本人随机条目与条目版本；解除不会恢复已取消关系。
func (s *Service) DeleteBlock(ctx context.Context, token, key, rid, id string, version int64) (err error) {
	defer s.completed("interaction_block_delete", rid, time.Now(), &err)
	if !ValidID(id) || version < 1 {
		return command.ErrInvalid
	}
	d := command.Definition{Kind: "interaction_block.delete", Method: "DELETE", ResourcePath: "/api/v1/me/interaction-blocks/" + id, ActorKind: "student", Access: command.Ordinary, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, a command.Subject) (p command.Discovery, e error) {
		var rows []entity.Block
		e = db.WithContext(ctx).Where("owner_id=? AND block_id=?", a.ID, id).Find(&rows).Error
		if len(rows) == 1 {
			p.Accounts = []string{rows[0].TargetID}
		}
		return
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error { return opaqueReady(ctx, tx.DB()) }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		var b entity.Block
		if e := tx.DB().WithContext(ctx).Where("owner_id=? AND block_id=?", tx.Actor().ID, id).Take(&b).Error; e != nil {
			return "", missingRow(e)
		}
		if b.Version != version {
			return "", command.ErrVersionConflict
		}
		return id, tx.DB().WithContext(ctx).Where("owner_id=? AND block_id=?", tx.Actor().ID, id).Delete(&entity.Block{}).Error
	}
	d.Resolve = func(context.Context, *command.Tx, string) error { return nil }
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: struct{ Version int64 }{version}})
	return
}
