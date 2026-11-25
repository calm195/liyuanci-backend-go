package randomprofile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

const collectionPath = "/api/v1/admin/random-profiles"

type mutation struct {
	target string
	before any
	after  any
}

func write[T any](ctx context.Context, s *Service, token, key, rid, operation, method, path string, request any,
	apply func(*command.Tx) (mutation, error),
) (out T, err error) {
	var ruleFailure error
	def := command.Definition{
		Kind:         "random_profile." + operation,
		Method:       method,
		ResourcePath: path,
		ActorKind:    "admin",
		AllowedRoles: []string{"super_admin"},
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(ctx context.Context, tx *command.Tx) error {
			return lockCatalog(tx.DB().WithContext(ctx), false)
		},
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			ruleFailure = nil
			result, err := apply(tx)
			if err != nil {
				for _, rule := range []error{ErrPoolEmpty, ErrNicknameTooLong, ErrDuplicateWord, ErrImageRejected, ErrImageBusy} {
					if errors.Is(err, rule) {
						// 命令引擎只返回其通用错误族，规则细节在同一次调用内保留。
						ruleFailure = rule
						return "", command.ErrConflict
					}
				}
				return "", err
			}
			before, err := json.Marshal(result.before)
			if err != nil {
				return "", err
			}
			after, err := json.Marshal(result.after)
			if err != nil {
				return "", err
			}
			action := entity.RandomProfileAction{
				ID: uuid.NewString(), ActorID: tx.Actor().ID, TargetID: result.target, Operation: operation,
				Before: before, After: after, At: tx.Now(),
			}
			return action.ID, tx.DB().WithContext(ctx).Create(&action).Error
		},
		Resolve: func(ctx context.Context, tx *command.Tx, id string) error {
			var action entity.RandomProfileAction
			if err := tx.DB().WithContext(ctx).Select("after").Where("id=?", id).Take(&action).Error; err != nil {
				return err
			}
			return json.Unmarshal(action.After, &out)
		},
	}
	_, err = s.engine.Execute(ctx, def, command.Input{
		Token: token, IdempotencyKey: key, RequestID: rid, Request: request,
	})
	if errors.Is(err, command.ErrConflict) && ruleFailure != nil {
		err = ruleFailure
	}
	if err != nil {
		var empty T
		return empty, err
	}
	return out, nil
}

func findAvatar(db *gorm.DB, id string, version int64) (row entity.RandomProfileAvatar, err error) {
	err = db.Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, command.ErrNotFound
	}
	if err == nil && row.Version != version {
		return row, command.ErrVersionConflict
	}
	return row, err
}

// EditAvatar 修改头像名称和启用状态，保留旧图片和稳定素材标识。
func (s *Service) EditAvatar(ctx context.Context, token, key, rid, id string, input EditAvatarInput) (Avatar, error) {
	input.Name = strings.TrimSpace(input.Name)
	if !canonicalID(id) || !validLabel(input.Name, 80) || input.Enabled == nil || input.ExpectedVersion < 1 {
		return Avatar{}, command.ErrInvalid
	}
	return write[Avatar](ctx, s, token, key, rid, "avatar.edit", "PUT", collectionPath+"/avatars/"+id, input, func(tx *command.Tx) (mutation, error) {
		db := tx.DB()
		row, err := findAvatar(db, id, input.ExpectedVersion)
		if err != nil {
			return mutation{}, err
		}
		before := avatarView(row)
		row.Name, row.Enabled = input.Name, *input.Enabled
		row.Version++
		row.UpdatedAt = tx.Now()
		if err = db.Save(&row).Error; err != nil {
			return mutation{}, err
		}
		if row.Enabled != before.Enabled {
			if err = validatePools(db); err != nil {
				return mutation{}, err
			}
		}
		return mutation{target: id, before: before, after: avatarView(row)}, nil
	})
}

func wordUnique(db *gorm.DB, id, kind, text string) error {
	var count int64
	q := db.Model(&entity.RandomProfileWord{}).Where("kind=? AND text=?", kind, text)
	if id != "" {
		q = q.Where("id<>?", id)
	}
	if err := q.Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrDuplicateWord
	}
	return nil
}

// CreateWord 创建并启用词条，保证新增组合符合昵称长度限制。
func (s *Service) CreateWord(ctx context.Context, token, key, rid string, input CreateWordInput) (Word, error) {
	input.Text = strings.TrimSpace(input.Text)
	if (input.Kind != adjective && input.Kind != noun) || !validLabel(input.Text, maxWord) {
		return Word{}, command.ErrInvalid
	}
	return write[Word](ctx, s, token, key, rid, "word.create", "POST", collectionPath+"/words", input, func(tx *command.Tx) (mutation, error) {
		db := tx.DB()
		if err := wordUnique(db, "", input.Kind, input.Text); err != nil {
			return mutation{}, err
		}
		row := entity.RandomProfileWord{
			ID: uuid.NewString(), Kind: input.Kind, Text: input.Text, Enabled: true, Version: 1, UpdatedAt: tx.Now(),
		}
		if err := db.Create(&row).Error; err != nil {
			return mutation{}, err
		}
		if err := validatePools(db); err != nil {
			return mutation{}, err
		}
		return mutation{target: row.ID, after: wordView(row)}, nil
	})
}

// EditWord 修改词条文本和启用状态，防止产生空池或超长昵称。
func (s *Service) EditWord(ctx context.Context, token, key, rid, id string, input EditWordInput) (Word, error) {
	input.Text = strings.TrimSpace(input.Text)
	if !canonicalID(id) || !validLabel(input.Text, maxWord) || input.Enabled == nil || input.ExpectedVersion < 1 {
		return Word{}, command.ErrInvalid
	}
	return write[Word](ctx, s, token, key, rid, "word.edit", "PUT", collectionPath+"/words/"+id, input, func(tx *command.Tx) (mutation, error) {
		db := tx.DB()
		var row entity.RandomProfileWord
		err := db.Where("id=?", id).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return mutation{}, command.ErrNotFound
		}
		if err != nil {
			return mutation{}, err
		}
		if row.Version != input.ExpectedVersion {
			return mutation{}, command.ErrVersionConflict
		}
		if err = wordUnique(db, id, row.Kind, input.Text); err != nil {
			return mutation{}, err
		}
		before := wordView(row)
		row.Text, row.Enabled = input.Text, *input.Enabled
		row.Version++
		row.UpdatedAt = tx.Now()
		if err = db.Save(&row).Error; err != nil {
			return mutation{}, err
		}
		if row.Enabled || before.Enabled {
			if err = validatePools(db); err != nil {
				return mutation{}, err
			}
		}
		return mutation{target: id, before: before, after: wordView(row)}, nil
	})
}

func validatePools(db *gorm.DB) error {
	catalog, err := listCatalog(db, true)
	if err != nil {
		return err
	}
	if len(catalog.Avatars) == 0 || len(catalog.Adjectives) == 0 || len(catalog.Nouns) == 0 {
		return ErrPoolEmpty
	}
	if longest(catalog.Adjectives)+1+longest(catalog.Nouns) > maxNickname {
		return ErrNicknameTooLong
	}
	return nil
}

func longest(words []Word) int {
	size := 0
	for _, word := range words {
		size = max(size, utf8.RuneCountInString(word.Text))
	}
	return size
}
