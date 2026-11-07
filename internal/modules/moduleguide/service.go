// Package moduleguide 管理模块说明的草稿、发布快照和本人确认状态。
package moduleguide

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Module 是一项面向小程序用户的纯文本说明。
type Module struct {
	Module string `json:"module"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// AdminGuide 同时返回草稿及已发布快照，编辑不会即时影响学生。
type AdminGuide struct {
	Version          int64    `json:"version"`
	PublishedVersion int64    `json:"published_version"`
	Modules          []Module `json:"modules"`
	PublishedModules []Module `json:"published_modules"`
}

// Guide 只包含已发布快照和本人的确认版本。
type Guide struct {
	Version             int64    `json:"version"`
	Modules             []Module `json:"modules"`
	AcknowledgedVersion int64    `json:"acknowledged_version"`
}

// SaveInput 使用完整草稿快照和乐观版本。
type SaveInput struct {
	ExpectedVersion int64    `json:"expected_version"`
	Modules         []Module `json:"modules"`
}

// PublishInput 明确指定待发布草稿版本。
type PublishInput struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// AckInput 只允许确认当前可读取的发布版本。
type AckInput struct {
	Version int64 `json:"version"`
}

// Service 借用数据库连接与命令引擎，不拥有其生命周期。
type Service struct {
	db     *gorm.DB
	engine *command.Engine
	log    *zap.Logger
	cfg    command.Config
}

// New 验证依赖，普通构造不创建或迁移数据。
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

func validModules(modules []Module) bool {
	if len(modules) == 0 || len(modules) > 12 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range modules {
		if item.Module == "" || len(item.Module) > 40 || seen[item.Module] ||
			strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Body) == "" ||
			!utf8.ValidString(item.Title) || !utf8.ValidString(item.Body) ||
			utf8.RuneCountInString(item.Title) > 40 || utf8.RuneCountInString(item.Body) > 2000 {
			return false
		}
		for _, r := range item.Module {
			if (r < 'a' || r > 'z') && r != '_' {
				return false
			}
		}
		seen[item.Module] = true
	}
	return true
}

func row(ctx context.Context, db *gorm.DB, lock bool) (entity.ModuleGuide, error) {
	var result entity.ModuleGuide
	query := db.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Where("id=1").Take(&result).Error
	return result, err
}

func adminProjection(value entity.ModuleGuide) (out AdminGuide, err error) {
	out.Version = value.Version
	out.PublishedVersion = value.PublishedVersion
	err = json.Unmarshal(value.Modules, &out.Modules)
	if err == nil {
		err = json.Unmarshal(value.PublishedModules, &out.PublishedModules)
	}
	return out, err
}

func studentProjection(ctx context.Context, db *gorm.DB, id string) (out Guide, err error) {
	value, err := row(ctx, db, false)
	if err != nil {
		return out, err
	}
	out.Version = value.PublishedVersion
	if err = json.Unmarshal(value.PublishedModules, &out.Modules); err != nil {
		return out, err
	}
	var acknowledgement entity.ModuleGuideAcknowledgement
	err = db.WithContext(ctx).Where("account_id=?", id).Take(&acknowledgement).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	out.AcknowledgedVersion = acknowledgement.Version
	return out, err
}

// Save 校验完整草稿快照，重试复用命令回执，发布内容保持独立快照。
func (s *Service) Save(ctx context.Context, token, key, rid string, input SaveInput) (out AdminGuide, err error) {
	if input.ExpectedVersion < 1 || !validModules(input.Modules) {
		return out, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, input, input.ExpectedVersion, false)
}

// Publish 只发布当前草稿，重复命令不会生成额外发布版本。
func (s *Service) Publish(ctx context.Context, token, key, rid string, input PublishInput) (out AdminGuide, err error) {
	if input.ExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, input, input.ExpectedVersion, true)
}

func (s *Service) write(ctx context.Context, token, key, rid string, input any, expected int64, publish bool) (out AdminGuide, err error) {
	kind, method, path := "moduleguide.save", "PUT", "/api/v1/admin/module-guide"
	if publish {
		kind, method, path = "moduleguide.publish", "POST", path+"/publish"
	}
	definition := command.Definition{
		Kind:         kind,
		Method:       method,
		ResourcePath: path,
		ActorKind:    "admin",
		AllowedRoles: []string{"super_admin"},
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(ctx context.Context, tx *command.Tx) error {
			_, e := row(ctx, tx.DB(), true)
			return e
		},
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			value, e := row(ctx, tx.DB(), false)
			if e != nil {
				return "", e
			}
			if value.Version != expected {
				return "", command.ErrVersionConflict
			}
			updates := map[string]any{"version": value.Version + 1}
			if publish {
				var modules []Module
				if json.Unmarshal(value.Modules, &modules) != nil || !validModules(modules) {
					return "", command.ErrInvalid
				}
				updates["published_version"] = value.Version
				updates["published_modules"] = value.Modules
			} else {
				encoded, e := json.Marshal(input.(SaveInput).Modules)
				if e != nil {
					return "", e
				}
				updates["modules"] = json.RawMessage(encoded)
			}
			return "00000000-0000-4000-8000-000000000001", tx.DB().WithContext(ctx).Model(&entity.ModuleGuide{}).Where("id=1").Updates(updates).Error
		},
		Resolve: func(ctx context.Context, tx *command.Tx, _ string) error {
			value, e := row(ctx, tx.DB(), false)
			if e == nil {
				out, e = adminProjection(value)
			}
			return e
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: input})
	if err != nil {
		out = AdminGuide{}
	}
	return out, err
}

// Acknowledge 确认与本人账号绑定，发布变化后旧版本确认不能覆盖新版本。
func (s *Service) Acknowledge(ctx context.Context, token, key, rid string, input AckInput) (out Guide, err error) {
	if input.Version < 1 {
		return out, command.ErrInvalid
	}
	definition := command.Definition{
		Kind:         "moduleguide.ack",
		Method:       "PUT",
		ResourcePath: "/api/v1/me/module-guide/ack",
		ActorKind:    "student",
		Access:       command.Restricted,
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(ctx context.Context, tx *command.Tx) error {
			_, e := row(ctx, tx.DB(), true)
			return e
		},
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			value, e := row(ctx, tx.DB(), false)
			if e != nil {
				return "", e
			}
			if value.PublishedVersion != input.Version {
				return "", command.ErrVersionConflict
			}
			e = tx.DB().WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "account_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"version"}),
			}).Create(&entity.ModuleGuideAcknowledgement{
				AccountID: tx.Actor().ID,
				Version:   input.Version,
			}).Error
			return tx.Actor().ID, e
		},
		Resolve: func(ctx context.Context, tx *command.Tx, _ string) error {
			var e error
			out, e = studentProjection(ctx, tx.DB(), tx.Actor().ID)
			return e
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: input})
	if err != nil {
		out = Guide{}
	}
	return out, err
}
