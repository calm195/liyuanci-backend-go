// Package dictionary 管理三端共享的展示名称，不改变固定代码对应的业务规则。
package dictionary

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Item 是可维护的展示元数据；停用不删除历史标签，也不改变业务权限。
type Item struct {
	Type      string `json:"type"`
	Code      string `json:"code"`
	Label     string `json:"label"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
}

// Snapshot 使用全局版本标识一份一致的展示字典。
type Snapshot struct {
	Version int64  `json:"version"`
	Items   []Item `json:"items"`
}

// SaveInput 只允许修改现有代码的展示字段。
type SaveInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Label           string `json:"label"`
	SortOrder       int    `json:"sort_order"`
	Enabled         *bool  `json:"enabled"`
}

// Service 借用数据库与命令引擎，普通构造不初始化数据库。
type Service struct {
	db     *gorm.DB
	engine *command.Engine
	log    *zap.Logger
	cfg    command.Config
}

// New 校验依赖并隔离ORM日志，避免展示审计内容被重复打印。
func New(db *gorm.DB, log *zap.Logger, cfg command.Config) (*Service, error) {
	engine, err := command.New(db, log, cfg)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:     db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}),
		engine: engine,
		log:    log,
		cfg:    cfg,
	}, nil
}

func validInput(kind, code string, input SaveInput) bool {
	if kind == "" || code == "" || len(kind) > 80 || len(code) > 80 ||
		!validDisplayFields(input.ExpectedVersion, input.Label) || input.Enabled == nil ||
		input.SortOrder < 0 || input.SortOrder > 100000 {
		return false
	}
	for _, r := range kind + code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	// 系统状态和角色必须始终可见，停用不承担授权含义。
	if !*input.Enabled && kind != "resource_feedback" {
		return false
	}
	return true
}

func validDisplayFields(version int64, label string) bool {
	return version > 0 && utf8.ValidString(label) && label != "" && strings.TrimSpace(label) == label &&
		utf8.RuneCountInString(label) <= 80 && !strings.ContainsAny(label, "\n\r\t")
}

func head(ctx context.Context, db *gorm.DB, strength string) (entity.DisplayDictionary, error) {
	var row entity.DisplayDictionary
	q := db.WithContext(ctx)
	if strength != "" {
		q = q.Clauses(clause.Locking{Strength: strength})
	}
	err := q.Where("id=1").Take(&row).Error
	return row, err
}

func snapshot(ctx context.Context, db *gorm.DB) (out Snapshot, err error) {
	// 共享锁保证版本与条目来自同一发布，不会跨越并发写入。
	row, err := head(ctx, db, "SHARE")
	if err != nil {
		return out, err
	}
	var items []entity.DisplayDictionaryItem
	if err = db.WithContext(ctx).Order("type, sort_order, code").Find(&items).Error; err != nil {
		return out, err
	}
	out.Version = row.Version
	out.Items = make([]Item, 0, len(items))
	for _, v := range items {
		out.Items = append(out.Items, project(v))
	}
	return out, nil
}

func project(v entity.DisplayDictionaryItem) Item {
	return Item{Type: v.Type, Code: v.Code, Label: v.Label, SortOrder: v.SortOrder, Enabled: v.Enabled}
}

// Read 返回无个人信息的全量展示快照，停用项用于解释历史数据。
func (s *Service) Read(ctx context.Context, rid string) (out Snapshot, err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		s.log.Info("展示字典读取完成", zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		out, e = snapshot(ctx, tx)
		return e
	})
	if err != nil {
		return Snapshot{}, command.ErrUnavailable
	}
	return out, nil
}

// Save 在同一命令事务中保存名称、发布版本与审计，重试不会重复修改。
func (s *Service) Save(ctx context.Context, token, key, rid, kind, code string, input SaveInput) (out Snapshot, err error) {
	if !validInput(kind, code, input) {
		return out, command.ErrInvalid
	}
	definition := command.Definition{
		Kind:         "dictionary.save",
		Method:       "PUT",
		ResourcePath: "/api/v1/admin/dictionaries/" + kind + "/" + code,
		ActorKind:    "admin",
		AllowedRoles: []string{"super_admin"},
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(ctx context.Context, tx *command.Tx) error {
			_, e := head(ctx, tx.DB(), "UPDATE")
			return e
		},
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			h, e := head(ctx, tx.DB(), "")
			if e != nil {
				return "", e
			}
			if h.Version != input.ExpectedVersion {
				return "", command.ErrVersionConflict
			}
			var item entity.DisplayDictionaryItem
			e = tx.DB().WithContext(ctx).Where("type=? AND code=?", kind, code).Take(&item).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return "", command.ErrNotFound
			}
			if e != nil {
				return "", e
			}
			before, e := json.Marshal(project(item))
			if e != nil {
				return "", e
			}
			item.Label = input.Label
			item.SortOrder = input.SortOrder
			item.Enabled = *input.Enabled
			item.Version = h.Version + 1
			if e = tx.DB().WithContext(ctx).Save(&item).Error; e != nil {
				return "", e
			}
			if e = tx.DB().WithContext(ctx).Model(&entity.DisplayDictionary{}).Where("id=1").Update("version", item.Version).Error; e != nil {
				return "", e
			}
			after, e := json.Marshal(project(item))
			if e != nil {
				return "", e
			}
			e = tx.DB().WithContext(ctx).Create(&entity.DisplayDictionaryAction{Version: item.Version, ActorID: tx.Actor().ID, Before: before, After: after, At: tx.Now()}).Error
			return "00000000-0000-4000-8000-000000000001", e
		},
		Resolve: func(ctx context.Context, tx *command.Tx, _ string) error {
			var e error
			out, e = snapshot(ctx, tx.DB())
			return e
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: input})
	if err != nil {
		out = Snapshot{}
	}
	return out, err
}
